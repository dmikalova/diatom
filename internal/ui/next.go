package ui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/reviewui"
	"github.com/dmikalova/diatom/internal/roster"
	"github.com/dmikalova/diatom/internal/tui"
)

// itemKind is what an item of Next asks of the human. Next offers a goal
// ready to finish first, as the work is done and the goals waiting for it
// can start once it lands; then a plan, which holds a whole goal up, and a
// question, which holds a task; and a review, which holds up nothing, last.
type itemKind int

const (
	itemPlan itemKind = iota
	itemQuestion
	itemFinish
	itemReview
)

// item is one thing Next asks of the human.
type item struct {
	kind itemKind
	row  *goalRow
	q    *queue.Question
	// task asked the question.
	task *queue.Task
}

func (it item) id() string {
	if it.q != nil {
		return it.row.goal.Name + " question " + it.q.ID
	}
	return fmt.Sprintf("%s %d", it.row.goal.Name, it.kind)
}

// nextArea is one part of an item that takes the keyboard, in the order tab
// moves through them: what it is about, the item itself, and the answer or
// what can be done.
type nextArea int

const (
	areaContext nextArea = iota
	areaBody
	areaAnswer
	nextAreas
)

// Next is everything waiting on the human across the repo, one item at a
// time: plans to approve, questions, goals ready to finish and the hunks
// finishing them brought, then the other hunks to review, each level in the
// nav's order of goals. Answering one moves on to
// the first left; nothing else changes the item shown.
type Next struct {
	ctx    context.Context
	env    Env
	status *Status

	items []item
	// cur is the id of the item shown.
	cur  string
	area nextArea
	// scroll is how far down the item's text is shown, and room how many of
	// its lines showed last time, for scrolling by half.
	scroll, room int
	answer       textarea.Model
	// confirm is the item a first enter on an empty answer asked to sign
	// off, and act the action selected on a goal ready to finish.
	confirm string
	act     int
	// earlier counts each goal's answered questions, from the last reload.
	earlier map[string]int
	// later are the items the human put off: they wait behind the rest.
	later map[string]bool
	// more is set while the human says what more a goal ready to finish
	// needs, in the answer box.
	more bool
	// only is the goal seen to from its page, "" for all of Next: its items
	// are the only ones shown, in Next's order.
	only string
	// stats is each finishing goal's diff stat, from the last reload, and
	// statCache each stat by the commit it is of.
	stats, statCache map[string]string
	// reviews are the reviewers of the goals whose hunks Next has shown.
	reviews map[string]*reviewui.Model
	// bounds are the lines each area started on at the last render.
	bounds [nextAreas]int

	width, height int
	// flash and err are what the human's last action came to, kept until
	// they do something else; loadErr is why the last reload failed.
	flash        string
	err, loadErr error
}

// The answer box's hints, for a question and for a plan.
const (
	answerHint = "Your answer. enter sends it, shift+enter adds a line, esc goes back."
	manualHint = "enter twice with nothing typed once it's done. Or write what happened, such as " +
		"an error, and enter sends it. esc goes back."
	planHint = "What to change in the plan. enter sends it back to grilling, shift+enter adds a " +
		"line, esc goes back."
	moreHint = "What more the goal needs before it lands. enter adds it as a task, shift+enter " +
		"adds a line, esc goes back."
)

// NewNext loads what waits on the human, from the status's rows.
func NewNext(ctx context.Context, env Env, status *Status) *Next {
	area := tui.TextBox()
	n := &Next{
		ctx:       ctx,
		env:       env,
		status:    status,
		answer:    area,
		area:      areaBody,
		stats:     map[string]string{},
		statCache: map[string]string{},
		reviews:   map[string]*reviewui.Model{},
		width:     80,
		height:    24,
	}
	n.reload()
	return n
}

// reload rebuilds the items from the status's rows, reloaded first.
func (n *Next) reload() {
	n.loadErr = nil
	var tiers [4][]item
	n.earlier = map[string]int{}
	for i := range n.status.rows {
		r := &n.status.rows[i]
		if planReady(r) {
			tiers[itemPlan] = append(tiers[itemPlan], item{kind: itemPlan, row: r})
		}
		if r.questions > 0 {
			tiers[itemQuestion] = append(tiers[itemQuestion], n.questions(r)...)
		}
		if readyToFinish(*r) && !n.status.landing(r.goal.Name) {
			tiers[itemFinish] = append(tiers[itemFinish], item{kind: itemFinish, row: r})
			n.stats[r.goal.Name] = n.diffStat(r.goal)
		}
		switch {
		case r.landingReview > 0:
			// What finishing the goal brought holds its landing up, as a
			// goal ready to finish does.
			tiers[itemFinish] = append(tiers[itemFinish], item{kind: itemReview, row: r})
		case r.toReview > 0 && !r.intake:
			tiers[itemReview] = append(tiers[itemReview], item{kind: itemReview, row: r})
		}
	}
	n.items = slices.Concat(tiers[itemFinish], tiers[itemPlan], tiers[itemQuestion],
		tiers[itemReview])
	// What the human put off waits behind everything else.
	slices.SortStableFunc(n.items, func(a, b item) int {
		return cmp.Compare(boolInt(n.later[a.id()]), boolInt(n.later[b.id()]))
	})
	if n.shown() == nil && len(n.items) > 0 {
		// The item shown is gone, such as a goal just landed: the next one
		// opens on itself, as moving on does, whichever area had the
		// keyboard.
		n.cur = n.first()
		n.scroll, n.confirm, n.act, n.more = 0, "", 0, false
		n.area = areaBody
		n.answer.Blur()
	}
	n.area = min(n.area, nextArea(n.areas()-1))
	if it := n.shown(); it != nil && it.kind == itemReview {
		if rv := n.reviewer(it.row.goal.Name); rv != nil {
			rv.Refresh()
		}
	}
}

// first is the item to show next: the first waiting, or, while a goal is
// seen to from its page, the first of its own, in the same order, "" for
// none.
func (n *Next) first() string {
	for _, it := range n.items {
		if n.only == "" || it.row.goal.Name == n.only {
			return it.id()
		}
	}
	return ""
}

// shownReviewer is the reviewer of the item shown, nil when it isn't a
// review.
func (n *Next) shownReviewer() *reviewui.Model {
	it := n.shown()
	if it == nil || it.kind != itemReview {
		return nil
	}
	return n.reviews[it.row.goal.Name]
}

// reviewer is the goal's reviewer, opened the first time it is needed.
func (n *Next) reviewer(goal string) *reviewui.Model {
	if rv, ok := n.reviews[goal]; ok {
		return rv
	}
	rv, err := n.env.reviewer(n.ctx, goal)
	if err != nil {
		n.loadErr = err
		return nil
	}
	n.reviews[goal] = rv
	return rv
}

// questions are a goal's open questions still unanswered, and they count
// its answered ones.
func (n *Next) questions(r *goalRow) []item {
	store, name := n.env.Store, r.goal.Name
	qs, err := store.Questions(name, queue.QuestionOpen)
	if err != nil {
		n.loadErr = err
	}
	var items []item
	for _, q := range qs {
		if q.Answer != "" {
			// Answered, and waiting for its task to take the answer in.
			n.earlier[name]++
			continue
		}
		it := item{kind: itemQuestion, row: r, q: q}
		if t, err := store.Task(name, q.Task); err == nil {
			it.task = t
		}
		items = append(items, it)
	}
	if closed, err := store.Questions(name, queue.QuestionClosed); err == nil {
		n.earlier[name] += len(closed)
	}
	return items
}

// readyToFinish reports whether a goal waits on the human to finish it: all
// its work done and reviewed, or done but not landed.
func readyToFinish(r goalRow) bool {
	if r.intake || r.notes > 0 {
		// Triage may add work to it from the notes.
		return false
	}
	if r.goal.State == queue.GoalDone {
		l := r.landing
		return l == nil || l.Landing == nil || l.Landing.How == ""
	}
	name, _ := goalStatus(r)
	return name == "ready to finish"
}

// shown is the item on screen, nil when nothing waits.
func (n *Next) shown() *item {
	for i := range n.items {
		if n.items[i].id() == n.cur {
			return &n.items[i]
		}
	}
	return nil
}

// areas is how many of an item's areas take the keyboard.
func (n *Next) areas() int {
	it := n.shown()
	switch {
	case it == nil:
		return 1
	case it.kind == itemReview:
		return 2
	}
	return int(nextAreas)
}

// typing reports whether the answer box has the keyboard.
func (n *Next) typing() bool {
	it := n.shown()
	switch {
	case it == nil:
		return false
	case it.kind == itemReview:
		rv := n.reviews[it.row.goal.Name]
		return n.area == areaBody && rv != nil && rv.Editing()
	}
	return n.area == areaAnswer &&
		(it.kind == itemQuestion || (it.kind == itemFinish || it.kind == itemPlan) && n.more)
}

// setArea moves the keyboard to an area, and the cursor with it.
func (n *Next) setArea(a nextArea) tea.Cmd {
	n.area = min(a, nextArea(n.areas()-1))
	n.answer.Blur()
	if n.typing() {
		return n.answer.Focus()
	}
	return nil
}

// moveOn shows the first item left once one is answered or approved.
func (n *Next) moveOn() tea.Cmd {
	n.status.reload()
	n.cur = ""
	n.reload()
	n.scroll, n.confirm, n.act, n.more = 0, "", 0, false
	n.answer.Reset()
	if len(n.items) == 0 {
		n.flash += "; nothing else waits on you"
	}
	return n.setArea(areaBody)
}

// nextKey is what a key in Next asks of the app: to go back to the nav, or
// to open a goal.
type nextKey struct {
	cmd  tea.Cmd
	back bool
	open string
}

func (n *Next) key(msg tea.KeyPressMsg) nextKey {
	k := msg.String()
	it := n.shown()
	if it == nil {
		return nextKey{back: k == "esc" || k == "left"}
	}
	// Everything but a second press of the key that asked to sign off.
	if (n.area != areaAnswer || k != "enter") &&
		(it.kind != itemPlan || k != approveKey) &&
		(it.kind != itemQuestion || k != sameKey) {
		n.confirm = ""
	}
	switch n.area {
	case areaContext:
		switch k {
		case "esc", "left":
			return nextKey{back: true}
		case "enter", "space", " ", "right", "l":
			return nextKey{open: it.row.goal.Name}
		}
	case areaBody:
		return n.bodyKey(*it, msg)
	case areaAnswer:
		if it.kind == itemFinish && n.more {
			return n.moreKey(*it, msg)
		}
		if it.kind == itemPlan && n.more {
			return n.commentKey(*it, msg)
		}
		if it.kind == itemFinish {
			return nextKey{cmd: n.finishKey(*it, k)}
		}
		if it.kind == itemPlan {
			return nextKey{cmd: n.planKey(*it, k)}
		}
		return n.answerKey(*it, msg)
	}
	return nextKey{}
}

// bodyKey hands a key to the item being read: the reviewer's own keys, the
// decisions a plan or a question can be done with while reading it, and
// scrolling.
func (n *Next) bodyKey(it item, msg tea.KeyPressMsg) nextKey {
	k := msg.String()
	switch {
	case it.kind == itemReview:
		return n.reviewKey(it, msg)
	case it.kind == itemPlan && slices.Contains([]string{approveKey, commentKey, laterKey}, k):
		// A plan is decided on as a hunk is, while reading it.
		return nextKey{cmd: n.planKey(it, k)}
	case it.kind == itemQuestion && k == laterKey:
		return nextKey{cmd: n.putOff(it)}
	case it.kind == itemQuestion && k == sameKey:
		return nextKey{cmd: n.sameAnswer(it)}
	}
	switch k {
	case "esc", "left":
		return nextKey{back: true}
	case "j", "down":
		n.scrollBy(1)
	case "k", "up":
		n.scrollBy(-1)
	case "space", " ":
		n.scrollBy(max(n.room/2, 1))
	case "shift+space":
		n.scrollBy(-max(n.room/2, 1))
	case "pgdown":
		n.scrollBy(max(n.room-1, 1))
	case "pgup":
		n.scrollBy(-max(n.room-1, 1))
	}
	return nextKey{}
}

func (n *Next) scrollBy(d int) { n.scroll = max(n.scroll+d, 0) }

// reviewKey hands a key to the goal's reviewer. Once a decision leaves the
// goal nothing to review, Next moves on.
func (n *Next) reviewKey(it item, msg tea.KeyPressMsg) nextKey {
	rv := n.reviewer(it.row.goal.Name)
	if rv == nil {
		return nextKey{back: msg.String() == "esc"}
	}
	if k := msg.String(); !rv.Editing() && (k == "esc" || k == "left") {
		return nextKey{back: true}
	}
	before := rv.Pending()
	cmd := rv.Key(msg)
	if rv.Pending() != before {
		n.status.reload()
		n.reload()
		if f := n.first(); f != "" && f != it.id() {
			// A decision is a point to move on at: something more urgent,
			// such as a plan, comes first, in the order Next keeps.
			n.cur, n.scroll, n.confirm, n.act, n.more = f, 0, "", 0, false
			n.area = areaBody
		}
	}
	return nextKey{cmd: cmd}
}

func (n *Next) answerKey(it item, msg tea.KeyPressMsg) nextKey {
	if cmd, ok := tui.Cut(&n.answer, msg); ok {
		return nextKey{cmd: cmd}
	}
	switch msg.String() {
	case "esc":
		return nextKey{cmd: n.setArea(areaBody)}
	case "enter":
		text := strings.TrimSpace(n.answer.Value())
		if text == "" && !it.q.Manual {
			return nextKey{}
		}
		if text == "" {
			if n.confirm != it.id() {
				n.confirm = it.id()
				n.flash = "enter again to say the steps are done"
				return nextKey{}
			}
			text = "Done."
		}
		if err := n.env.Store.Answer(it.row.goal.Name, it.q.ID, text, n.env.Now()); err != nil {
			n.err = err
			return nextKey{}
		}
		n.flash = fmt.Sprintf("answered; task %s is ready again", it.q.Task)
		return nextKey{cmd: n.moveOn()}
	}
	var cmd tea.Cmd
	n.answer, cmd = n.answer.Update(msg)
	return nextKey{cmd: cmd}
}

// The keys of a plan, as the reviewer's: approve it, or comment on what to
// change, which sends it back.
const (
	approveKey = "a"
	commentKey = "c"
)

// planActions are what can be done with a plan handed in: approving it,
// commenting on it, and putting it off.
func planActions(it item) []action {
	return []action{
		{approveKey, fmt.Sprintf("Approve: %d workstreams, %d tasks",
			len(it.row.plan.Workstreams), len(it.row.plan.Tasks))},
		{commentKey, "Comment: say what to change, and grilling revises the plan"},
		{laterKey, "Later: ask again once the rest are done"},
	}
}

// planKey moves through a plan's actions, and does one on a second enter,
// or on its own key: a second a approves, c opens the comment box.
func (n *Next) planKey(it item, k string) tea.Cmd {
	acts := planActions(it)
	pick := k == "enter" || k == "space" || k == " "
	if pick && n.act < len(acts) {
		k = acts[n.act].key
	}
	switch k {
	case approveKey:
		return n.approve(it, pick)
	case commentKey:
		n.more = true
		n.answer.Reset()
		n.area = areaAnswer
		return n.answer.Focus()
	case laterKey:
		return n.putOff(it)
	case "esc":
		return n.setArea(areaBody)
	case "j", "down":
		n.act = min(n.act+1, len(acts)-1)
	case "k", "up":
		n.act = max(n.act-1, 0)
	}
	return nil
}

// approve signs a plan off once it is asked for twice (ADR 0010).
func (n *Next) approve(it item, byEnter bool) tea.Cmd {
	g := it.row.goal
	if n.confirm != it.id() {
		n.confirm = it.id()
		again := approveKey
		if byEnter {
			again = "enter"
		}
		n.flash = fmt.Sprintf("%s again to approve %s: %d workstreams, %d tasks", again,
			g.Name, len(it.row.plan.Workstreams), len(it.row.plan.Tasks))
		return nil
	}
	cfg, err := n.env.config()
	if err == nil {
		err = plan.Approve(n.ctx, n.env.Store, cfg, g.Name, n.env.Now())
	}
	if err != nil {
		n.err = err
		return nil
	}
	n.flash = g.Name + " is approved and active"
	return n.moveOn()
}

// commentKey types what to change in a plan, and sends it back to grilling
// once enter sends it (ADR 0010).
func (n *Next) commentKey(it item, msg tea.KeyPressMsg) nextKey {
	if cmd, ok := tui.Cut(&n.answer, msg); ok {
		return nextKey{cmd: cmd}
	}
	switch msg.String() {
	case "esc":
		n.more = false
		n.answer.Reset()
		n.answer.Blur()
		return nextKey{}
	case "enter":
		text := strings.TrimSpace(n.answer.Value())
		if text == "" {
			n.flash = "say what to change, or esc goes back"
			return nextKey{}
		}
		g := it.row.goal
		if _, err := intake.Write(plan.FeedbackDir(n.env.Store.GoalDir(g.Name)),
			intake.Intake{Source: "questions", Created: n.env.Now(), Text: text}); err != nil {
			n.err = err
			return nextKey{}
		}
		n.flash = "sent back to " + g.Name + "'s grilling with your comment"
		return nextKey{cmd: n.moveOn()}
	}
	var cmd tea.Cmd
	n.answer, cmd = n.answer.Update(msg)
	return nextKey{cmd: cmd}
}

// finishKey moves through a finishing goal's actions, and does one on a
// second enter, or on its own key pressed twice.
func (n *Next) finishKey(it item, k string) tea.Cmd {
	acts := finishActions(it)
	chose := func(key string) bool {
		return k == key || (k == "enter" || k == "space" || k == " ") && n.act < len(acts) &&
			acts[n.act].key == key
	}
	switch {
	case chose(laterKey):
		return n.putOff(it)
	case chose(moreKey):
		n.more = true
		n.answer.Reset()
		return n.answer.Focus()
	}
	switch k {
	case "esc":
		return n.setArea(areaBody)
	case "j", "down":
		n.act = min(n.act+1, max(len(acts)-1, 0))
		return nil
	case "k", "up":
		n.act = max(n.act-1, 0)
		return nil
	case "enter", "space", " ":
		if n.act >= len(acts) {
			return nil
		}
		cmd, _ := n.status.act(it.row, acts[n.act].key)
		// The action's own key confirms it too, but enter is what was pressed.
		n.status.flash = strings.Replace(
			n.status.flash,
			"press "+acts[n.act].key+" again",
			"enter again",
			1,
		)
		n.acted()
		return cmd
	}
	cmd, ok := n.status.act(it.row, k)
	if ok {
		n.acted()
	}
	return cmd
}

// laterKey puts an item off, to come back once the rest is seen to, and
// moreKey says what more a goal ready to finish needs.
const (
	laterKey = "l"
	moreKey  = "m"
)

// finishActions are what can be done with a goal ready to finish: its
// actions, asking for more work, and putting it off.
func finishActions(it item) []action {
	return append(actions(it.row),
		action{moreKey, "More work: say what it still needs, as a task of its own"},
		action{laterKey, "Later: ask again once the rest are done"})
}

// moreKey types what more a goal needs, and adds it as a task of the goal's
// once enter sends it.
func (n *Next) moreKey(it item, msg tea.KeyPressMsg) nextKey {
	if cmd, ok := tui.Cut(&n.answer, msg); ok {
		return nextKey{cmd: cmd}
	}
	switch msg.String() {
	case "esc":
		n.more = false
		n.answer.Reset()
		n.answer.Blur()
		return nextKey{}
	case "enter":
		text := strings.TrimSpace(n.answer.Value())
		if text == "" {
			n.flash = "say what more it needs, or esc goes back"
			return nextKey{}
		}
		if err := finish.MoreWork(n.env.Store, it.row.goal, text, n.env.now()); err != nil {
			n.err = err
			return nextKey{}
		}
		n.flash = it.row.goal.Name + " has more work: it comes back once that is done and reviewed"
		return nextKey{cmd: n.moveOn()}
	}
	var cmd tea.Cmd
	n.answer, cmd = n.answer.Update(msg)
	return nextKey{cmd: cmd}
}

// sameKey answers a question with the answer an earlier one already got.
const sameKey = "s"

// sameAnswer hands a question the answer an earlier one already got, once it
// is asked for twice. The task's own answers come first, then the rest of
// the goal's.
func (n *Next) sameAnswer(it item) tea.Cmd {
	src := n.lastAnswered(it)
	if src == nil {
		n.flash = it.row.goal.Name + " has no earlier answer to repeat"
		return nil
	}
	if n.confirm != it.id() {
		n.confirm = it.id()
		n.flash = fmt.Sprintf("%s again to answer it as question %s was on %s: %s", sameKey,
			src.ID, stamp(src.Answered), roster.Clip(src.Answer))
		return nil
	}
	text := fmt.Sprintf("Question %s asked this too, and the answer was:\n\n%s", src.ID, src.Answer)
	if err := n.env.Store.Answer(it.row.goal.Name, it.q.ID, text, n.env.Now()); err != nil {
		n.err = err
		return nil
	}
	n.flash = fmt.Sprintf("answered as question %s was; task %s is ready again", src.ID, it.q.Task)
	return n.moveOn()
}

// lastAnswered is the newest answer the goal already has, its own task's
// first, nil when it has none.
func (n *Next) lastAnswered(it item) *queue.Question {
	var answered []*queue.Question
	for _, st := range []queue.QuestionState{queue.QuestionOpen, queue.QuestionClosed} {
		qs, err := n.env.Store.Questions(it.row.goal.Name, st)
		if err != nil {
			n.err = err
			continue
		}
		for _, q := range qs {
			if q.ID != it.q.ID && q.Answer != "" {
				answered = append(answered, q)
			}
		}
	}
	own := slices.DeleteFunc(slices.Clone(answered), func(q *queue.Question) bool {
		return q.Task != it.q.Task
	})
	if len(own) > 0 {
		answered = own
	}
	if len(answered) == 0 {
		return nil
	}
	return slices.MaxFunc(answered, func(a, b *queue.Question) int {
		return a.Answered.Compare(b.Answered)
	})
}

// lower is what shows below the item: the answer box, or what can be done
// with it.
func (n *Next) lower(it item, w int) []string {
	var acts []action
	hint := answerHint
	switch it.kind {
	case itemQuestion:
		if it.q.Manual {
			hint = manualHint
		}
		return n.answerBox(hint, w)
	case itemFinish:
		acts, hint = finishActions(it), moreHint
	case itemPlan:
		acts, hint = planActions(it), planHint
	default:
		return nil
	}
	if n.more {
		return n.answerBox(hint, w)
	}
	lines := make([]string, 0, len(acts))
	for i, a := range acts {
		mark := "  "
		if i == n.act {
			mark = tui.Color("› ", tui.Accent)
		}
		lines = append(lines, mark+tui.Color(a.key, tui.Yellow)+"  "+a.label)
	}
	return lines
}

// answerBox is the answer box sized to w, hinting what to write.
func (n *Next) answerBox(hint string, w int) []string {
	n.answer.Placeholder = hint
	n.answer.SetWidth(w)
	n.answer.SetHeight(max(min(n.answer.LineCount()+1, n.height/3), 3))
	return strings.Split(n.answer.View(), "\n")
}

// putOff moves the item behind everything else that waits, and moves on.
func (n *Next) putOff(it item) tea.Cmd {
	if n.later == nil {
		n.later = map[string]bool{}
	}
	n.later[it.id()] = true
	n.flash = it.row.goal.Name + " waits until the rest are done"
	if it.kind == itemQuestion {
		n.flash = "question " + it.q.ID + " waits until the rest are done"
	}
	return n.moveOn()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// acted reloads what waits once an action has changed a goal.
func (n *Next) acted() {
	n.status.reload()
	n.reload()
}

// render shows the item: what it is about, the item, and the answer box or
// what can be done, each marked when it has the keyboard.
func (n *Next) render(focused bool, foot []string) string {
	for _, err := range []error{n.err, n.loadErr} {
		if err != nil {
			var lines []string
			for l := range strings.SplitSeq(err.Error(), "\n") {
				lines = append(lines, tui.Color(l, tui.Red))
			}
			foot = append(lines, foot...)
		}
	}
	if n.flash != "" {
		foot = append(foot, tui.Color(n.flash, tui.Cyan))
	}
	it := n.shown()
	if it == nil {
		return strings.Join(append([]string{n.idle()}, foot...), "\n")
	}
	w := max(n.width-2, 20)
	ctxLines := n.contextLines(*it, w)
	lower := n.lower(*it, w)
	used := len(ctxLines) + 2 + len(foot)
	if len(lower) > 0 {
		used += len(lower) + 1
	}
	n.room = max(n.height-used, 3)
	var shown []string
	if rv := n.reviews[it.row.goal.Name]; it.kind == itemReview && rv != nil {
		rv.SetSize(w, n.room)
		shown = strings.Split(strings.TrimRight(rv.Render(), "\n"), "\n")
	} else {
		body := strings.Split(ansi.Wordwrap(strings.TrimSpace(n.bodyText(*it)), w, ""), "\n")
		n.scroll = min(n.scroll, max(len(body)-n.room, 0))
		shown = body[n.scroll:min(n.scroll+n.room, len(body))]
		if n.scroll > 0 {
			shown = append([]string{tui.Dim("↑ more above")}, shown[1:]...)
		}
		if n.scroll+n.room < len(body) {
			shown = append(shown[:len(shown)-1], tui.Dim("↓ more below · space scrolls"))
		}
	}

	var out []string
	mark := func(a nextArea, lines []string) {
		n.bounds[a] = len(out)
		bar, rule := "  ", tui.Dim("──")
		if focused && n.area == a {
			bar, rule = tui.Color("▌ ", tui.Accent), tui.Color("▌", tui.Accent)+tui.Dim("─")
		}
		for _, l := range lines {
			if ruleStart(l) {
				// A rule, such as the reviewer's under its hunk's head, runs
				// on to the nav's border and joins it.
				out = append(out, rule+l)
				continue
			}
			out = append(out, bar+l)
		}
	}
	mark(areaContext, ctxLines)
	out = append(out, tui.Dim(strings.Repeat("─", w+2)))
	mark(areaBody, shown)
	if len(lower) > 0 {
		out = append(out, tui.Dim(strings.Repeat("─", w+2)))
		mark(areaAnswer, lower)
	}
	return strings.Join(append(out, foot...), "\n")
}

// idle is what Next says with nothing waiting.
func (n *Next) idle() string {
	for _, r := range n.status.rows {
		name, _ := goalStatus(r)
		if len(r.activeWork) > 0 || name == "queued" {
			return "Nothing needs you · work in progress"
		}
	}
	return "Nothing needs you"
}

// contextLines say what an item is about: its goal, where it stands, what it
// is for, who asked, and how to see more.
func (n *Next) contextLines(it item, w int) []string {
	r := it.row
	name := r.goal.Name
	title := r.goal.Title
	if title == "" {
		title = name
	}
	state, c := goalStatus(*r)
	var lines []string
	if r.intake {
		lines = append(lines, tui.Bold("Intake"), tui.Dim("triage is sorting what you sent"))
	} else {
		lines = append(lines, strings.Split(ansi.Wordwrap(tui.Bold(title), w, ""), "\n")...)
		lines = append(lines, tui.Color(state, c)+tui.Dim(" · "+name))
		if d := r.description; d != "" {
			lines = append(lines, clipLines(d, w, aboutLines)...)
		}
	}
	switch it.kind {
	case itemQuestion:
		if it.task != nil {
			asked := fmt.Sprintf("Asked by task %s: %s", it.task.ID, it.task.Title)
			if it.q.Manual {
				asked = fmt.Sprintf(
					"%s Steps to do by hand, for task %s: %s",
					emoji("👤"),
					it.task.ID,
					it.task.Title,
				)
			}
			if r.intake {
				asked = "Asked while sorting: " + roster.Clip(it.task.Body)
			}
			lines = append(lines, clipLines(asked, w, 2)...)
		}
		lines = append(lines, tui.Dim("waiting since "+stamp(it.q.Created)))
	case itemPlan:
		lines = append(lines, tui.Dim("Grilling's plan, waiting for you to approve it"))
	case itemFinish:
		lines = append(lines, tui.Dim("All its work is done and reviewed"))
	case itemReview:
		lines = append(lines, tui.Dim(fmt.Sprintf("%d hunks wait for review", r.toReview)))
	}
	more := "enter opens the goal"
	if r.intake {
		more = "enter opens the intake"
	}
	if it.kind == itemQuestion {
		more = fmt.Sprintf("%s puts it off · %s answers it as the last one was · %s",
			laterKey, sameKey, more)
	}
	if e := n.earlier[name]; e > 0 {
		more = fmt.Sprintf("%d earlier answers · %s", e, more)
	}
	return append(lines, tui.Dim(more))
}

// aboutLines is how much of what a goal is for shows above an item: a
// paragraph, and the goal's page has the rest.
const aboutLines = 8

// clipLines wraps s to w, keeping at most max lines.
func clipLines(s string, w, most int) []string {
	lines := hang(s, w)
	if len(lines) > most {
		lines = lines[:most]
		lines[most-1] = ansi.Truncate(lines[most-1], w-1, "") + "…"
	}
	return lines
}

// hangIndent is how far a paragraph's later lines sit in from its first, so
// they read as part of it.
const hangIndent = "  "

// hang wraps a paragraph to w, its later lines indented under its first.
func hang(s string, w int) []string {
	lines := strings.Split(ansi.Wordwrap(s, max(w-len(hangIndent), 10), ""), "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = hangIndent + lines[i]
	}
	return lines
}

// bodyText is the item itself.
func (n *Next) bodyText(it item) string {
	switch it.kind {
	case itemQuestion:
		return it.q.Text
	case itemPlan:
		return plan.Describe(it.row.plan)
	case itemFinish:
		return n.finishText(it)
	}
	return fmt.Sprintf("%d hunks of %s's commits wait for your review.", it.row.toReview,
		it.row.goal.Name)
}

// finishText is what finishing a goal lands, and what it lets start.
func (n *Next) finishText(it item) string {
	g := it.row.goal
	var b strings.Builder
	if g.State == queue.GoalDone {
		b.WriteString(landingLine(*it.row) + "\n\n")
	} else {
		fmt.Fprintf(&b, "Every task is done and every hunk reviewed. Landing it first merges in "+
			"what %s gained since: an agent resolves anything that conflicts, and its resolution comes back "+
			"for review. Its commits then go onto %s's tip, the gate runs, and it lands.\n\n", g.Base, g.Base)
	}
	if stat := n.stats[g.Name]; stat != "" {
		fmt.Fprintf(&b, "%s against %s: %s\n", g.IntegrationBranch(), g.Base, stat)
	}
	var unblocks []string
	for _, r := range n.status.rows {
		if slices.Contains(r.goal.After, g.Name) {
			unblocks = append(unblocks, r.goal.Name)
		}
	}
	if len(unblocks) > 0 {
		fmt.Fprintf(&b, "\nFinishing unblocks: %s\n", strings.Join(unblocks, ", "))
	}
	return b.String()
}

// diffStat sums up what the goal's integration branch changes.
func (n *Next) diffStat(g *queue.Goal) string {
	repo := git.Repo{Dir: n.env.Store.Repo()}
	sha, err := repo.RevParse(n.ctx, g.IntegrationBranch())
	if err != nil {
		return ""
	}
	if s, ok := n.statCache[sha]; ok {
		return s
	}
	out, err := repo.Run(n.ctx, "diff", "--shortstat", g.Base+"..."+sha)
	if err != nil {
		return ""
	}
	n.statCache[sha] = strings.TrimSpace(out)
	return n.statCache[sha]
}

// areaAt is the area on line y of the last render.
func (n *Next) areaAt(y int) nextArea {
	a := areaContext
	for i := range n.areas() {
		if y >= n.bounds[i] {
			a = nextArea(i)
		}
	}
	return a
}

// onScreen describes the item for an intake sent while it is shown.
func (n *Next) onScreen() (goal, context string) {
	it := n.shown()
	if it == nil {
		return "", ""
	}
	g := it.row.goal
	if it.row.intake {
		goal = ""
	} else {
		goal = g.Name
	}
	switch it.kind {
	case itemQuestion:
		what := "question"
		if it.q.Manual {
			what = "manual steps"
		}
		return goal, fmt.Sprintf(
			"%s %s of %s (%q), from task %s: %s",
			what,
			it.q.ID,
			g.Name,
			g.Title,
			it.q.Task,
			excerpt(it.q.Text),
		)
	case itemPlan:
		return goal, fmt.Sprintf("the plan of %s (%q), waiting for approval: %s", g.Name, g.Title,
			excerpt(it.row.plan.Summary))
	case itemFinish:
		return goal, fmt.Sprintf("%s (%q), ready to finish", g.Name, g.Title)
	}
	return goal, fmt.Sprintf("%s (%q), with %d hunks to review", g.Name, g.Title, it.row.toReview)
}

// excerptWidth is how much of what was on screen an intake carries.
const excerptWidth = 300

func excerpt(s string) string {
	line := strings.Join(strings.Fields(s), " ")
	if r := []rune(line); len(r) > excerptWidth {
		line = string(r[:excerptWidth]) + "…"
	}
	return line
}
