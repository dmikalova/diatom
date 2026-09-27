package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/reviewui"
	"github.com/dmikalova/diatom/internal/roster"
	"github.com/dmikalova/diatom/internal/tui"
)

// itemKind is what an item of Next asks of the human, in the order Next
// offers them: a plan holds a whole goal up and a question a task, while a
// goal ready to finish holds up the goals waiting for it, and a review holds
// up nothing.
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
// time: plans to sign off, questions, goals ready to finish, then hunks to
// review, each level in the nav's order of goals. Answering one moves on to
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
	planHint   = "enter with nothing typed signs the plan off. Or write what to change, and enter " +
		"sends it back to grilling. esc goes back."
)

// NewNext loads what waits on the human, from the status's rows.
func NewNext(ctx context.Context, env Env, status *Status) *Next {
	area := textarea.New()
	area.ShowLineNumbers = false
	area.Prompt = ""
	editKeys(&area)
	area.SetStyles(plainStyles())
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
		if r.goal.State == queue.GoalPlanning && r.plan != nil && !r.sentBack {
			tiers[itemPlan] = append(tiers[itemPlan], item{kind: itemPlan, row: r})
		}
		if r.questions > 0 {
			tiers[itemQuestion] = append(tiers[itemQuestion], n.questions(r)...)
		}
		if readyToFinish(*r) {
			tiers[itemFinish] = append(tiers[itemFinish], item{kind: itemFinish, row: r})
			n.stats[r.goal.Name] = n.diffStat(r.goal)
		}
		if r.toReview > 0 && !r.intake {
			tiers[itemReview] = append(tiers[itemReview], item{kind: itemReview, row: r})
		}
	}
	n.items = slices.Concat(tiers[:]...)
	if n.shown() == nil && len(n.items) > 0 {
		n.cur = n.items[0].id()
	}
	if it := n.shown(); it != nil && it.kind == itemReview {
		if rv := n.reviewer(it.row.goal.Name); rv != nil {
			rv.Refresh()
		}
	}
}

// reviewer is the goal's reviewer, opened the first time it is needed.
func (n *Next) reviewer(goal string) *reviewui.Model {
	if rv, ok := n.reviews[goal]; ok {
		return rv
	}
	rv, err := reviewui.New(n.ctx, n.env.Store, goal)
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
// its work done and reviewed, or done and laid out but not landed.
func readyToFinish(r goalRow) bool {
	if r.intake {
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
	return n.area == areaAnswer && (it.kind == itemQuestion || it.kind == itemPlan)
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

// moveOn shows the first item left once one is answered or signed off.
func (n *Next) moveOn() tea.Cmd {
	n.status.reload()
	n.cur = ""
	n.reload()
	n.scroll, n.confirm, n.act = 0, "", 0
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
	if n.area != areaAnswer || k != "enter" {
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
		if it.kind == itemReview {
			return n.reviewKey(*it, msg)
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
		case "pgdown":
			n.scrollBy(max(n.room-1, 1))
		case "pgup":
			n.scrollBy(-max(n.room-1, 1))
		}
	case areaAnswer:
		if it.kind == itemFinish {
			return nextKey{cmd: n.finishKey(*it, k)}
		}
		return n.answerKey(*it, msg)
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
		if n.shown() == nil || n.shown().id() != it.id() {
			n.scroll = 0
		}
	}
	return nextKey{cmd: cmd}
}

func (n *Next) answerKey(it item, msg tea.KeyPressMsg) nextKey {
	if cmd, ok := cut(&n.answer, msg); ok {
		return nextKey{cmd: cmd}
	}
	switch msg.String() {
	case "esc":
		return nextKey{cmd: n.setArea(areaBody)}
	case "enter":
		text := strings.TrimSpace(n.answer.Value())
		if it.kind == itemPlan {
			return nextKey{cmd: n.decide(it, text)}
		}
		if text == "" {
			return nextKey{}
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

// decide signs a plan off on a second enter with nothing typed, or sends
// what was typed back to grilling as the changes wanted (ADR 0010).
func (n *Next) decide(it item, text string) tea.Cmd {
	g := it.row.goal
	if text == "" {
		if n.confirm != it.id() {
			n.confirm = it.id()
			n.flash = fmt.Sprintf("enter again to sign off %s: %d workstreams, %d tasks",
				g.Name, len(it.row.plan.Workstreams), len(it.row.plan.Tasks))
			return nil
		}
		cfg, err := config.Load(n.env.Store.Repo(), n.env.Paths)
		if err == nil {
			err = plan.Approve(n.ctx, n.env.Store, cfg, g.Name, n.env.Now())
		}
		if err != nil {
			n.err = err
			return nil
		}
		n.flash = g.Name + " is signed off and active"
		return n.moveOn()
	}
	if _, err := intake.Write(plan.FeedbackDir(n.env.Store.GoalDir(g.Name)),
		intake.Intake{Source: "questions", Created: n.env.Now(), Text: text}); err != nil {
		n.err = err
		return nil
	}
	n.flash = "sent back to " + g.Name + "'s grilling with your changes"
	return n.moveOn()
}

// finishKey moves through a finishing goal's actions, and does one on a
// second enter, or on its own key pressed twice.
func (n *Next) finishKey(it item, k string) tea.Cmd {
	acts := actions(it.row)
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
			foot = append([]string{tui.Color(err.Error(), tui.Red)}, foot...)
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
	var lower []string
	switch it.kind {
	case itemQuestion, itemPlan:
		hint := answerHint
		if it.kind == itemPlan {
			hint = planHint
		}
		n.answer.Placeholder = hint
		n.answer.SetWidth(w)
		n.answer.SetHeight(max(min(n.answer.LineCount()+1, n.height/3), 3))
		lower = strings.Split(n.answer.View(), "\n")
	case itemFinish:
		for i, a := range actions(it.row) {
			mark := "  "
			if i == n.act {
				mark = tui.Color("› ", tui.Cyan)
			}
			lower = append(lower, mark+tui.Color(a.key, tui.Yellow)+"  "+a.label)
		}
	}
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
		bar := "  "
		if focused && n.area == a {
			bar = tui.Color("▌ ", tui.Cyan)
		}
		for _, l := range lines {
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
			if r.intake {
				asked = "Asked while sorting: " + roster.Clip(it.task.Body)
			}
			lines = append(lines, clipLines(asked, w, 2)...)
		}
	case itemPlan:
		lines = append(lines, tui.Dim("Grilling's plan, waiting for you to sign it off"))
	case itemFinish:
		lines = append(lines, tui.Dim("All its work is done and reviewed"))
	case itemReview:
		lines = append(lines, tui.Dim(fmt.Sprintf("%d hunks wait for review", r.toReview)))
	}
	more := "enter opens the goal"
	if r.intake {
		more = "enter opens the intake"
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
	lines := strings.Split(ansi.Wordwrap(s, w, ""), "\n")
	if len(lines) > most {
		lines = lines[:most]
		lines[most-1] = ansi.Truncate(lines[most-1], w-1, "") + "…"
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
			"for review. It is then laid out on %s's tip, the gate runs, and it lands.\n\n", g.Base, g.Base)
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
		return goal, fmt.Sprintf(
			"question %s of %s (%q), from task %s: %s",
			it.q.ID,
			g.Name,
			g.Title,
			it.q.Task,
			excerpt(it.q.Text),
		)
	case itemPlan:
		return goal, fmt.Sprintf("the plan of %s (%q), waiting for sign-off: %s", g.Name, g.Title,
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
