// Package panes holds the workspace's panes besides the reviewer (ADR 0007):
// status, questions and intake. Each is a separate Bubble Tea program that
// reads and writes .diatom/ state, so any of them can restart without the
// others noticing.
package panes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
	"github.com/dmikalova/diatom/internal/tui"
)

// refreshEvery is how often a pane rereads the state.
const refreshEvery = 3 * time.Second

// Env is what every pane reads and writes.
type Env struct {
	// Store is the repo's; a workspace shows one repo (ADR 0007).
	Store *queue.Store
	Focus focus.File
	Paths config.Paths
	Now   func() time.Time
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// goalRow is one goal in the status pane.
type goalRow struct {
	repo       string
	goal       *queue.Goal
	counts     map[queue.State]int
	questions  int
	toReview   int
	activeWork []string
	// plan is the plan waiting for sign-off, and grilling the grilling
	// task's state, for a goal in planning.
	plan     *plan.Plan
	grilling queue.State
	// sentBack is set once the human has asked for changes to the plan
	// that grilling hasn't taken in yet.
	sentBack bool
	// landing is a done goal's layout, nil when it has none.
	landing *finish.Result
	// intake marks the row of the intake triage is sorting, which isn't a
	// goal of the human's.
	intake bool
	// waiting names the goals this one waits for that aren't finished.
	waiting []string
	// ready counts the tasks that could start now, on a workstream with no
	// session running.
	ready int
	// cost is what the goal's sessions have cost so far, and taskCost each
	// task's even share of the sessions that worked on it.
	cost     float64
	taskCost map[string]float64
}

// Status shows every goal in the repo, the intake triage is sorting, and the
// sessions running now, and sets the focus the other panes follow.
type Status struct {
	ctx   context.Context
	env   Env
	rows  []goalRow
	sel   int
	focus focus.Focus

	// hunks caches each commit's hunk count; commits never change.
	hunks map[string]int
	// costs caches what each settled session cost, by its directory.
	costs map[string]sessionCost

	// confirm names the key and goal a first press asked to confirm, such
	// as a sign-off.
	confirm string
	// busy says what a background job, such as laying a goal out, is
	// doing; it takes no other job until that one ends.
	busy string

	// health says what keeps the scheduler from starting work, when
	// something does: it isn't running, or every pass fails.
	health string

	// detail is the row opened with enter: its tasks, or one of them.
	detail *detail
	// top is the first line of the list on screen.
	top int

	width, height int
	flash         string
	err           error
}

// NewStatus loads the status pane.
func NewStatus(ctx context.Context, env Env) *Status {
	s := &Status{
		ctx: ctx, env: env, hunks: map[string]int{}, costs: map[string]sessionCost{},
		width: 80, height: 24,
	}
	s.reload()
	return s
}

func (s *Status) reload() {
	s.err = nil
	fc, err := s.env.Focus.Read()
	if err != nil {
		s.err = err
	}
	s.focus = fc
	store := s.env.Store
	s.health = health(store)
	goals, err := store.Goals()
	if err != nil {
		s.err = err
		return
	}
	var rows []goalRow
	// What the human sent comes first, while triage has any of it.
	if g, err := store.Goal(queue.IntakeGoal); err == nil {
		row, err := s.row(store, g)
		if err != nil {
			s.err = err
		}
		row.intake = true
		rows = append(rows, row)
	}
	for _, g := range goals {
		if g.State == queue.GoalFinished {
			continue
		}
		row, err := s.row(store, g)
		if err != nil {
			s.err = err
			continue
		}
		rows = append(rows, row)
	}
	keep := s.selected()
	s.rows = rows
	if keep != nil {
		s.sel = max(slices.IndexFunc(rows, func(r goalRow) bool {
			return r.goal.Name == keep.goal.Name
		}), 0)
	}
	s.sel = min(s.sel, max(len(rows)-1, 0))
	s.reloadDetail()
}

func (s *Status) row(store *queue.Store, g *queue.Goal) (goalRow, error) {
	row := goalRow{
		repo:    store.Repo(),
		goal:    g,
		counts:  map[queue.State]int{},
		waiting: store.Waiting(g),
	}
	tasks, err := store.Tasks(g.Name)
	if err != nil {
		return row, err
	}
	var commits []string
	switch g.State {
	case queue.GoalPlanning:
		if row.plan, err = plan.Load(store.GoalDir(g.Name)); err != nil {
			return row, err
		}
		sent, err := intake.Pending(plan.FeedbackDir(store.GoalDir(g.Name)))
		if err != nil {
			return row, err
		}
		row.sentBack = len(sent) > 0
	case queue.GoalDone:
		if row.landing, err = finish.Load(store.GoalDir(g.Name)); err != nil {
			return row, err
		}
	}
	for _, t := range tasks {
		if t.Kind == queue.Grilling {
			row.grilling = t.State
		}
		row.counts[t.State]++
		if t.State == queue.Active {
			row.activeWork = append(row.activeWork, t.Workstream)
		}
		for _, sha := range t.Commits {
			if !slices.Contains(commits, sha) {
				commits = append(commits, sha)
			}
		}
	}
	qs, err := store.Questions(g.Name, queue.QuestionOpen)
	if err != nil {
		return row, err
	}
	for _, q := range qs {
		if q.Answer == "" {
			row.questions++
		}
	}
	if row.toReview, err = s.toReview(store, g.Name, commits); err != nil {
		return row, err
	}
	slices.Sort(row.activeWork)
	row.activeWork = slices.Compact(row.activeWork)
	row.cost, row.taskCost = s.goalCost(store.SessionsDir(g.Name))
	if g.State == queue.GoalActive && len(row.waiting) == 0 {
		for _, t := range schedule.Ready(tasks) {
			if !slices.Contains(row.activeWork, t.Workstream) {
				row.ready++
			}
		}
	}
	return row, nil
}

// sessionCost is what one session cost and the tasks it worked on.
type sessionCost struct {
	usd   float64
	tasks []string
}

// goalCost adds up what the sessions in root have cost, in all and for each
// task: a session's cost is shared evenly among its tasks. A session still
// running has no cost yet; one stopped and resumed counts only what it cost
// after its last start.
func (s *Status) goalCost(root string) (float64, map[string]float64) {
	dirs, _ := filepath.Glob(filepath.Join(root, "*"))
	total, perTask := 0.0, map[string]float64{}
	for _, dir := range dirs {
		c, ok := s.costs[dir]
		if !ok {
			spec, err := session.Load(dir)
			if err != nil {
				continue
			}
			var res struct {
				Usage struct {
					CostUSD float64 `json:"costUSD"`
				} `json:"usage"`
			}
			ended, err := session.ReadResult(dir, &res)
			if err != nil || !ended {
				continue
			}
			c = sessionCost{usd: res.Usage.CostUSD, tasks: spec.Tasks}
			if st, err := session.LoadState(dir); err == nil {
				c.usd += st.CommitCostUSD
				if st.Settled {
					s.costs[dir] = c
				}
			}
		}
		total += c.usd
		for _, t := range c.tasks {
			perTask[t] += c.usd / float64(len(c.tasks))
		}
	}
	return total, perTask
}

// toReview counts the hunks of commits nobody has approved or rejected.
func (s *Status) toReview(store *queue.Store, goal string, commits []string) (int, error) {
	rev := review.Store{Dir: store.GoalDir(goal)}
	repo := git.Repo{Dir: store.Repo()}
	total := 0
	for _, sha := range commits {
		n, ok := s.hunks[sha]
		if !ok {
			hunks, err := review.Hunks(s.ctx, repo, sha)
			if err != nil {
				return 0, err
			}
			n = len(hunks)
			s.hunks[sha] = n
		}
		rec, err := rev.Load(sha)
		if err != nil {
			return 0, err
		}
		decided := 0
		for _, r := range rec.Hunks {
			if r.Decision != review.Defer {
				decided++
			}
		}
		total += max(n-decided, 0)
	}
	return total, nil
}

func (s *Status) selected() *goalRow {
	if s.sel < 0 || s.sel >= len(s.rows) {
		return nil
	}
	return &s.rows[s.sel]
}

// Init implements tea.Model.
func (s *Status) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (s *Status) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tickMsg:
		s.reload()
		return s, tick()
	case jobMsg:
		s.reload()
		s.busy, s.flash, s.err = "", msg.flash, msg.err
	case tea.KeyPressMsg:
		return s.updateKey(msg)
	}
	return s, nil
}

// goalKeys act on a goal, so not on the intake row.
var goalKeys = []string{"p", "s", "d", "D", "F", "U", "f"}

func (s *Status) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s.flash = ""
	if s.detail != nil {
		return s.updateDetail(msg)
	}
	row := s.selected()
	key := msg.String()
	if row != nil && row.intake && slices.Contains(goalKeys, key) {
		s.flash = "intake isn't a goal: enter shows what triage is doing with it"
		return s, nil
	}
	if row != nil {
		if cmd, ok := s.act(row, key); ok {
			return s, cmd
		}
	}
	switch key {
	case "q", "ctrl+c":
		return s, tea.Quit
	case "j", "down":
		s.sel = min(s.sel+1, max(len(s.rows)-1, 0))
	case "k", "up":
		s.sel = max(s.sel-1, 0)
	case "enter", "space", " ", "right", "l":
		if row != nil {
			if !row.intake {
				s.setFocus(focus.Focus{Goal: row.goal.Name})
			}
			s.openDetail(row)
		}
	case "f":
		if row != nil {
			s.setFocus(focus.Focus{Goal: row.goal.Name})
		}
	case "esc":
		s.setFocus(focus.Focus{})
	case "r":
		s.reload()
	}
	s.confirm = ""
	return s, nil
}

// act does what one of a goal's keys does, and reports whether key is one.
func (s *Status) act(row *goalRow, key string) (tea.Cmd, bool) {
	switch key {
	case "p":
		s.toggleParked(row)
	case "s":
		s.signOff(row)
		return nil, true
	case "d", "D", "F", "U":
		return s.finishKey(row, key), true
	default:
		return nil, false
	}
	s.confirm = ""
	return nil, true
}

// action is one thing a goal's menu offers, and the key that does it.
type action struct{ key, label string }

// actions lists what can be done with a goal now, the step it is waiting
// for first.
func actions(r *goalRow) []action {
	if r == nil || r.intake {
		return nil
	}
	g := r.goal
	var a []action
	switch g.State {
	case queue.GoalPlanning:
		if r.plan != nil && !r.sentBack {
			a = append(a, action{"s", fmt.Sprintf("Sign off the plan: %d workstreams, %d tasks",
				len(r.plan.Workstreams), len(r.plan.Tasks))})
		}
	case queue.GoalActive:
		left := r.counts[queue.Pending] + r.counts[queue.Active] + r.counts[queue.Blocked]
		if left == 0 && r.toReview == 0 {
			a = append(a, action{"d", "Mark it done and lay it out for landing"})
		} else {
			a = append(a, action{"D", fmt.Sprintf(
				"Mark it done with work left: %d tasks not done, %d hunks to review",
				left, r.toReview)})
		}
		a = append(a, action{"p", "Park it: start nothing new"})
	case queue.GoalParked:
		a = append(a, action{"p", "Resume it"})
	case queue.GoalDone:
		if l := r.landing; l == nil || l.Landing == nil || l.Landing.How == "" {
			a = append(a,
				action{"F", "Open its stacked pull requests"},
				action{"U", "Push it straight to " + g.Base})
		}
	}
	return a
}

// signOff signs the selected goal's plan off on a second s (ADR 0010).
func (s *Status) signOff(row *goalRow) {
	name := row.repo + "/" + row.goal.Name
	if row.plan == nil {
		s.flash, s.confirm = row.goal.Name+" has no plan to sign off", ""
		return
	}
	if row.sentBack {
		s.flash, s.confirm = row.goal.Name+"'s plan went back with changes: grilling hands in the next", ""
		return
	}
	if !s.confirmed("s", name, fmt.Sprintf(
		"press s again to sign off %s: %d workstreams, %d tasks",
		row.goal.Name,
		len(row.plan.Workstreams),
		len(row.plan.Tasks),
	)) {
		return
	}
	cfg, err := config.Load(row.repo, s.env.Paths)
	if err == nil {
		err = plan.Approve(s.ctx, queue.Open(row.repo), cfg, row.goal.Name, s.env.Now())
	}
	if err != nil {
		s.err = err
		return
	}
	s.flash = row.goal.Name + " is signed off and active"
	s.reload()
}

// confirmed reports whether this press of key confirms the one before it
// on the same goal. A first press only shows prompt.
func (s *Status) confirmed(key, name, prompt string) bool {
	if s.confirm != key+" "+name {
		s.confirm, s.flash = key+" "+name, prompt
		return false
	}
	s.confirm = ""
	return true
}

// jobMsg ends a background job.
type jobMsg struct {
	flash string
	err   error
}

// finishKey handles the keys that end and land a goal (ADR 0003), each
// confirmed with a second press: d marks it done and lays it out, D does so
// with hunks unreviewed or tasks not done, F opens its pull requests, and U
// pushes it straight to its base branch. Laying out runs the gate, so the
// job runs in the background.
func (s *Status) finishKey(row *goalRow, key string) tea.Cmd {
	if s.busy != "" {
		s.flash = "still " + s.busy
		return nil
	}
	// The job gets its own copy: the rows are rendered while it runs.
	goal := *row.goal
	g, name := &goal, row.repo+"/"+row.goal.Name
	var prompt string
	switch key {
	case "d", "D":
		if g.State == queue.GoalDone {
			s.flash = g.Name + " is done already: F opens its pull requests, U pushes it"
			return nil
		}
		prompt = fmt.Sprintf(
			"press %s again to mark %s done and lay it out for landing",
			key,
			g.Name,
		)
		if key == "D" {
			prompt += ", unreviewed hunks and all"
		}
	default:
		if g.State != queue.GoalDone {
			s.flash = g.Name + " isn't done: press d to mark it done first"
			return nil
		}
		prompt = fmt.Sprintf("press F again to push %s and open its pull requests", g.Name)
		if key == "U" {
			prompt = fmt.Sprintf("press U again to push %s straight to %s", g.Name, g.Base)
		}
	}
	if !s.confirmed(key, name, prompt) {
		return nil
	}
	s.busy = "laying " + g.Name + " out and running the gate"
	store, paths, ctx := queue.Open(row.repo), s.env.Paths, s.ctx
	return func() tea.Msg {
		flash, err := runFinish(ctx, store, paths, g, key)
		return jobMsg{flash: flash, err: err}
	}
}

// runFinish does what finishKey confirmed.
func runFinish(
	ctx context.Context,
	s *queue.Store,
	paths config.Paths,
	g *queue.Goal,
	key string,
) (string, error) {
	if key == "d" || key == "D" {
		if err := finish.MarkDone(ctx, s, g, key == "D"); err != nil {
			return "", err
		}
	}
	res, err := finish.Ready(ctx, s, g)
	if err == nil && res == nil {
		var cfg *config.Config
		if cfg, err = config.Load(s.Repo(), paths); err == nil {
			res, err = finish.Build(
				ctx,
				s,
				g,
				finish.Options{Gate: cfg.Gate, Timeout: cfg.GateTimeout},
			)
		}
	}
	if err != nil {
		return "", err
	}
	remote := "origin"
	if res.Landing != nil && res.Landing.Remote != "" {
		remote = res.Landing.Remote
	}
	switch key {
	case "F":
		urls, err := finish.Land(ctx, s, g, res, finish.PRs, remote, false, finish.RunGH)
		return fmt.Sprintf("opened %s", strings.Join(urls, " ")), err
	case "U":
		_, err := finish.Land(ctx, s, g, res, finish.Push, remote, false, nil)
		return fmt.Sprintf("pushed %s to %s on %s", g.Name, g.Base, remote), err
	}
	return fmt.Sprintf("%s is done and laid out as %d pull requests", g.Name, len(res.Stack)), nil
}

// toggleParked parks an active goal or resumes a parked one. Parking stops
// new tasks and keeps everything else, so resuming loses nothing (ADR 0003).
func (s *Status) toggleParked(row *goalRow) {
	switch row.goal.State {
	case queue.GoalActive:
		row.goal.State = queue.GoalParked
		s.save(row, "parked")
	case queue.GoalParked:
		row.goal.State = queue.GoalActive
		s.save(row, "resumed")
	default:
		s.flash = fmt.Sprintf(
			"%s is %s; only an active or parked goal can be parked or resumed",
			row.goal.Name,
			row.goal.State,
		)
	}
}

func (s *Status) save(row *goalRow, what string) {
	if err := queue.Open(row.repo).SaveGoal(row.goal); err != nil {
		s.err = err
		return
	}
	s.flash = fmt.Sprintf("%s %s", row.goal.Name, what)
}

func (s *Status) setFocus(fc focus.Focus) {
	if err := s.env.Focus.Write(fc); err != nil {
		s.err = err
		return
	}
	s.focus = fc
	s.flash = "focused " + describe(fc)
}

func describe(fc focus.Focus) string {
	if fc.Goal == "" {
		return "the repo"
	}
	return fc.Goal
}

// View implements tea.Model. The repo and focus are in the title, which
// zellij shows on the pane's frame, so the pane itself is only the goals.
func (s *Status) View() tea.View {
	v := tea.NewView(s.render())
	v.AltScreen = true
	// The total comes first, so a narrow frame cuts the focus instead.
	total := 0.0
	for _, r := range s.rows {
		total += r.cost
	}
	v.WindowTitle = fmt.Sprintf("status · $%.2f · %s · focus %s",
		total, filepath.Base(s.env.Store.Repo()), describe(s.focus))
	return v
}

func (s *Status) render() string {
	if s.detail != nil {
		return s.renderDetail()
	}
	var b strings.Builder
	used := 0
	if s.health != "" {
		h := ansi.Wordwrap(s.health, max(s.width, 20), "")
		b.WriteString(tui.Color(h, tui.Red) + "\n\n")
		used = strings.Count(h, "\n") + 2
	}
	if len(s.rows) == 0 {
		b.WriteString(
			"No goals yet. Describe what you want in the intake pane, and triage turns it " +
				"into goals.\n",
		)
	}
	var lines []string
	first, last := 0, 0
	for i, r := range s.rows {
		var rb strings.Builder
		s.renderRow(&rb, i, r)
		if i == s.sel {
			first = len(lines)
		}
		lines = append(lines, strings.Split(strings.TrimRight(rb.String(), "\n"), "\n")...)
		if i == s.sel {
			last = len(lines) - 1
		}
	}
	foot := s.foot()
	b.WriteString(scroll(lines, first, last, &s.top, max(s.height-len(foot)-used, 3)))
	if len(foot) > 0 {
		b.WriteString("\n" + strings.Join(foot, "\n"))
	}
	return b.String()
}

// health says what keeps the scheduler from starting work, or "" when
// nothing does.
func health(store *queue.Store) string {
	if _, err := store.Scheduler(); errors.Is(err, queue.ErrNotRunning) {
		return "The scheduler isn't running, so no work starts: run `diatom run` in the scheduler tab."
	}
	st, err := store.Stuck()
	if err != nil || st == nil {
		return ""
	}
	return fmt.Sprintf("The scheduler is stuck, so no work starts, since %s: %s",
		st.Since.Local().Format("15:04"), st.Error)
}

// foot is what the last key and the background job said.
func (s *Status) foot() []string {
	var foot []string
	if s.err != nil {
		foot = append(foot, tui.Color(s.err.Error(), tui.Red))
	}
	if s.flash != "" {
		foot = append(foot, tui.Color(s.flash, tui.Cyan))
	}
	if s.busy != "" {
		foot = append(foot, tui.Color(s.busy+"…", tui.Yellow))
	}
	return foot
}

// scroll shows room lines, moved from *top just enough to show the lines
// from first to last, or first when they don't all fit.
func scroll(lines []string, first, last int, top *int, room int) string {
	if last >= *top+room {
		*top = last - room + 1
	}
	*top = min(*top, first)
	*top = max(min(*top, len(lines)-room), 0)
	return strings.Join(lines[*top:min(*top+room, len(lines))], "\n")
}

// renderRow renders one goal and what is running for it.
func (s *Status) renderRow(b *strings.Builder, i int, r goalRow) {
	mark := "  "
	if i == s.sel {
		mark = tui.Color("› ", tui.Cyan)
	}
	if r.intake {
		cost := ""
		if r.cost > 0 {
			cost = tui.Dim(fmt.Sprintf(" · $%.2f", r.cost))
		}
		fmt.Fprintf(b, "%s  %s %s%s\n", mark, tui.Bold("intake"), intakeLine(r), cost)
		s.renderActive(b, r)
		return
	}
	focused := " "
	if r.goal.Name == s.focus.Goal {
		focused = tui.Color("●", tui.Cyan)
	}
	fmt.Fprintf(b, "%s%s %s %s\n", mark, focused, tui.Bold(r.goal.Name), goalState(r))
	fmt.Fprintf(
		b,
		"      %s",
		tui.Dim(fmt.Sprintf(
			"tasks %d pending · %d active · %d blocked · %d done",
			r.counts[queue.Pending],
			r.counts[queue.Active],
			r.counts[queue.Blocked],
			r.counts[queue.Done],
		)),
	)
	if r.questions > 0 {
		b.WriteString(" · " + tui.Color(fmt.Sprintf("%d questions", r.questions), tui.Magenta))
	}
	if r.toReview > 0 {
		b.WriteString(" · " + tui.Color(fmt.Sprintf("%d to review", r.toReview), tui.Yellow))
	}
	if r.cost > 0 {
		b.WriteString(tui.Dim(fmt.Sprintf(" · $%.2f", r.cost)))
	}
	b.WriteString("\n")
	if r.goal.State == queue.GoalDone {
		b.WriteString("      " + landingLine(r) + "\n")
	}
	if r.goal.State == queue.GoalPlanning && len(r.waiting) == 0 {
		b.WriteString("      " + planningLine(r) + "\n")
	}
	s.renderActive(b, r)
}

// renderActive shows each running session's latest step.
func (s *Status) renderActive(b *strings.Builder, r goalRow) {
	for _, ws := range r.activeWork {
		name := ws
		if ws == "" {
			// Triage and grilling run on no workstream, in planning sessions.
			name, ws = "planning", "planning"
		}
		fmt.Fprintf(
			b,
			"      %s %s\n",
			tui.Color("▶ "+name, tui.Green),
			tui.Dim(lastEvent(r.repo, r.goal.Name, ws)),
		)
	}
}

// landingLine says how far a done goal is on its way upstream.
func landingLine(r goalRow) string {
	line := finish.Summary(r.goal, r.landing)
	switch l := r.landing; {
	case l == nil:
		return tui.Color(line, tui.Yellow)
	case l.Landing == nil || l.Landing.How == "":
		return tui.Color(line+" · F open PRs · U push", tui.Green)
	case l.Landing.Checks == finish.ChecksFailed || l.Landing.Error != "":
		return tui.Color(line, tui.Red)
	}
	return tui.Dim(line)
}

// planningLine says where a goal in planning stands.
func planningLine(r goalRow) string {
	switch {
	case r.sentBack:
		return tui.Color("plan sent back with your changes: grilling takes them next", tui.Blue)
	case r.plan != nil:
		return tui.Color(fmt.Sprintf("plan ready to sign off: %d workstreams, %d tasks",
			len(r.plan.Workstreams), len(r.plan.Tasks)), tui.Green)
	case r.grilling == queue.Blocked:
		return tui.Color("grilling: waiting on your answers", tui.Magenta)
	case r.grilling == queue.Active:
		return tui.Color("grilling: a round is running", tui.Blue)
	}
	return tui.Dim("grilling: the next round is queued")
}

// goalState is the goal's state as the human sees it, in its color.
func goalState(r goalRow) string {
	name, c := goalStatus(r)
	return tui.Color(name, c)
}

// goalStatus is the goal's state as the human sees it, and its color. An
// active goal is queued while it has work ready and no session running, and
// reviewing once every task is done while hunks are left to review; with
// those reviewed too it is ready to finish.
func goalStatus(r goalRow) (string, int) {
	if len(r.waiting) > 0 &&
		(r.goal.State == queue.GoalActive || r.goal.State == queue.GoalPlanning) {
		// Opening the goal says which goals it waits for.
		return "blocked", tui.Red
	}
	if r.goal.State != queue.GoalActive || len(r.activeWork) > 0 {
		return string(r.goal.State), stateColors[r.goal.State]
	}
	left := r.counts[queue.Pending] + r.counts[queue.Blocked]
	switch {
	case r.ready > 0:
		return "queued", tui.Yellow
	case left == 0 && r.counts[queue.Done] > 0 && r.toReview > 0:
		return "reviewing", tui.Cyan
	case left == 0 && r.counts[queue.Done] > 0:
		return "ready to finish", tui.Magenta
	}
	return string(r.goal.State), stateColors[r.goal.State]
}

var stateColors = map[queue.GoalState]int{
	queue.GoalActive: tui.Green, queue.GoalParked: tui.Yellow, queue.GoalPlanning: tui.Blue,
	queue.GoalDone: tui.Magenta,
}

// lastEvent is the latest step of a workstream's running session.
func lastEvent(repo, goal, ws string) string {
	dirs, err := filepath.Glob(filepath.Join(queue.Open(repo).SessionsDir(goal), "*-"+ws))
	if err != nil || len(dirs) == 0 {
		return ""
	}
	slices.Sort(dirs)
	events, err := session.ReadEvents(dirs[len(dirs)-1])
	if err != nil {
		return ""
	}
	st := steps(events)
	if len(st) == 0 {
		return ""
	}
	return oneLine(st[len(st)-1].summary, 90)
}

// Editing reports whether a job, such as laying a goal out, is still
// running.
func (s *Status) Editing() bool { return s.busy != "" }

// intakeLine says what the human sent that triage is still sorting.
func intakeLine(r goalRow) string {
	sorting := r.counts[queue.Pending] + r.counts[queue.Active]
	waiting := r.counts[queue.Blocked]
	var parts []string
	if sorting > 0 {
		parts = append(parts, fmt.Sprintf("%d being sorted", sorting))
	}
	if waiting > 0 {
		parts = append(
			parts,
			tui.Color(fmt.Sprintf("%d waiting on your answers", waiting), tui.Magenta),
		)
	}
	if len(parts) == 0 {
		return tui.Dim("all sorted")
	}
	return strings.Join(parts, tui.Dim(" · "))
}
