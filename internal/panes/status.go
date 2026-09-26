// Package panes holds the workspace's panes besides the reviewer (ADR 0007):
// status, questions and intake. Each is a separate Bubble Tea program that
// reads and writes .diatom/ state, so any of them can restart without the
// others noticing.
package panes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
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
	// landing is a done goal's layout, nil when it has none.
	landing *finish.Result
	// intake marks the row of the intake triage is sorting, which isn't a
	// goal of the human's.
	intake bool
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

	// viewing shows the selected goal's plan; confirm names the key and
	// goal a first press asked to confirm, such as a sign-off.
	viewing bool
	confirm string
	// busy says what a background job, such as laying a goal out, is
	// doing; it takes no other job until that one ends.
	busy string

	// detail is the row opened with enter: its tasks, or one of them.
	detail *detail

	width, height int
	flash         string
	err           error
}

// NewStatus loads the status pane.
func NewStatus(ctx context.Context, env Env) *Status {
	s := &Status{ctx: ctx, env: env, hunks: map[string]int{}, width: 80, height: 24}
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
	row := goalRow{repo: store.Repo(), goal: g, counts: map[queue.State]int{}}
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
	return row, nil
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
var goalKeys = []string{"p", "P", "s", "d", "D", "F", "U", "f"}

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
	switch key {
	case "q", "ctrl+c":
		return s, tea.Quit
	case "j", "down":
		s.sel = min(s.sel+1, max(len(s.rows)-1, 0))
	case "k", "up":
		s.sel = max(s.sel-1, 0)
	case "enter", "right", "l":
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
	case "p":
		if row != nil {
			s.toggleParked(row)
		}
	case "P":
		if row != nil {
			row.goal.Pinned = !row.goal.Pinned
			what := "unpinned"
			if row.goal.Pinned {
				what = "pinned"
			}
			s.save(row, what)
		}
	case "v":
		s.viewing = !s.viewing
	case "s":
		if row != nil {
			s.signOff(row)
		}
		return s, nil
	case "d", "D", "F", "U":
		if row != nil {
			return s, s.finishKey(row, msg.String())
		}
		return s, nil
	case "r":
		s.reload()
	}
	s.confirm = ""
	return s, nil
}

// signOff signs the selected goal's plan off on a second s (ADR 0010).
func (s *Status) signOff(row *goalRow) {
	name := row.repo + "/" + row.goal.Name
	if row.plan == nil {
		s.flash, s.confirm = row.goal.Name+" has no plan to sign off", ""
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
				finish.Options{Gate: cfg.Gate, Timeout: cfg.CommandTimeout},
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

// View implements tea.Model.
func (s *Status) View() tea.View {
	v := tea.NewView(s.render())
	v.AltScreen = true
	return v
}

func (s *Status) render() string {
	if s.detail != nil {
		return s.renderDetail()
	}
	var b strings.Builder
	b.WriteString(
		tui.Bold(
			filepath.Base(s.env.Store.Repo()),
		) + tui.Dim(
			" · focus "+describe(s.focus),
		) + "\n\n",
	)
	if len(s.rows) == 0 {
		b.WriteString(
			"No goals yet. Describe what you want in the intake pane, and triage turns it " +
				"into goals.\n",
		)
	}
	for i, r := range s.rows {
		s.renderRow(&b, i, r)
	}
	if s.err != nil {
		b.WriteString("\n" + tui.Color(s.err.Error(), tui.Red) + "\n")
	}
	if s.flash != "" {
		b.WriteString("\n" + tui.Color(s.flash, tui.Cyan) + "\n")
	}
	if s.busy != "" {
		b.WriteString("\n" + tui.Color(s.busy+"…", tui.Yellow) + "\n")
	}
	b.WriteString(
		"\n" + tui.Dim(
			"enter open · f focus · p park/resume · P pin · v view plan · s sign off · d done · "+
				"F open PRs · U push · esc focus the repo · q quit",
		),
	)
	return b.String()
}

// renderRow renders one goal and what is running for it.
func (s *Status) renderRow(b *strings.Builder, i int, r goalRow) {
	mark := "  "
	if i == s.sel {
		mark = tui.Color("› ", tui.Cyan)
	}
	if r.intake {
		fmt.Fprintf(b, "%s  %s %s\n", mark, tui.Bold("intake"), intakeLine(r))
		s.renderActive(b, r)
		return
	}
	focused := " "
	if r.goal.Name == s.focus.Goal {
		focused = tui.Color("●", tui.Cyan)
	}
	name := r.goal.Name
	if r.goal.Pinned {
		name += " 📌"
	}
	fmt.Fprintf(b, "%s%s %s %s\n", mark, focused, tui.Bold(name), stateColor(r.goal.State))
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
	b.WriteString("\n")
	if r.goal.State == queue.GoalDone {
		b.WriteString("      " + landingLine(r) + "\n")
	}
	if r.goal.State == queue.GoalPlanning {
		b.WriteString("      " + planningLine(r) + "\n")
		if s.viewing && i == s.sel && r.plan != nil {
			for l := range strings.SplitSeq(strings.TrimRight(plan.Describe(r.plan), "\n"), "\n") {
				b.WriteString("      " + tui.Dim("│ ") + l + "\n")
			}
		}
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
	case r.plan != nil:
		return tui.Color(fmt.Sprintf("plan ready: %d workstreams, %d tasks · v view · s sign off",
			len(r.plan.Workstreams), len(r.plan.Tasks)), tui.Green)
	case r.grilling == queue.Blocked:
		return tui.Color("grilling: waiting on your answers", tui.Magenta)
	case r.grilling == queue.Active:
		return tui.Color("grilling: a round is running", tui.Blue)
	}
	return tui.Dim("grilling: the next round is queued")
}

func stateColor(st queue.GoalState) string {
	c := map[queue.GoalState]int{
		queue.GoalActive: tui.Green, queue.GoalParked: tui.Yellow, queue.GoalPlanning: tui.Blue,
		queue.GoalDone: tui.Magenta,
	}[st]
	return tui.Color(string(st), c)
}

// lastEvent is the latest progress line of a workstream's running session.
func lastEvent(repo, goal, ws string) string {
	dirs, err := filepath.Glob(filepath.Join(queue.Open(repo).SessionsDir(goal), "*-"+ws))
	if err != nil || len(dirs) == 0 {
		return ""
	}
	slices.Sort(dirs)
	f, err := os.Open(filepath.Join(dirs[len(dirs)-1], "events.jsonl"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		last = sc.Text()
	}
	var ev struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(last), &ev) != nil {
		return ""
	}
	text, _, _ := strings.Cut(strings.TrimSpace(ev.Text), "\n")
	if len(text) > 90 {
		text = text[:90] + "…"
	}
	return text
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
