// Package ui is diatom's window (ADR 0007): a nav of Next, the intake and
// the repo's goals down the left, and a main pane showing what it selects.
// Everything it shows is read from .diatom/, and everything it changes is
// written there, so the scheduler running beside it needs no other channel.
package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/roster"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
	"github.com/dmikalova/diatom/internal/spend"
	"github.com/dmikalova/diatom/internal/tui"
)

// refreshEvery is how often a pane rereads the state.
const refreshEvery = 3 * time.Second

// Env is what the window reads and writes.
type Env struct {
	// Store is the repo's; a window shows one repo (ADR 0007).
	Store *queue.Store
	Paths config.Paths
	Now   func() time.Time
	// Runner runs the agent that settles a landing's conflicts, and Exe is
	// diatom, for the hooks it runs with; a nil Runner leaves such conflicts
	// unsettled.
	Runner runner.Runner
	Exe    string
}

// now is the window's clock.
func (e Env) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// goalRow is one goal in the status pane.
type goalRow struct {
	repo      string
	goal      *queue.Goal
	counts    map[queue.State]int
	questions int
	toReview  int
	// notes counts the comments on hunks of the goal the human approved,
	// which triage hasn't sorted yet: it may add work to the goal, so they
	// hold its landing up.
	notes int
	// settling are the workstreams of activeWork whose agent has ended,
	// while diatom gates, commits and merges its work.
	settling []string
	// landingReview counts the hunks of toReview that finishing the goal
	// brought: catching up with its base, and settling a rebase's
	// conflicts. They hold the goal's landing up, as its finishing does.
	landingReview int
	activeWork    []string
	// plan is the plan waiting for sign-off, and grilling the grilling
	// task's state, for a goal in planning.
	plan     *plan.Plan
	grilling queue.State
	// sentBack is set once the human has asked for changes to the plan
	// that grilling hasn't taken in yet.
	sentBack bool
	// landing is a done goal's layout, nil when it has none.
	landing *finish.Result
	// land is how the repo's config says its goals land: config.LandMerge,
	// config.LandPRs, or "" for either.
	land string
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
	// description says what the goal is for, in a paragraph.
	description string
	// latest is the latest step of each workstream's running session, read
	// once a reload rather than each time the window is drawn.
	latest map[string]string
}

// Status is where the repo's goals, and the intake triage is sorting, stand:
// the rows the nav lists, and a goal opened to its actions, tasks, sessions
// and steps.
type Status struct {
	ctx  context.Context
	env  Env
	rows []goalRow

	// hunks caches each commit's hunk count; commits never change.
	hunks map[string]int
	// spent reads what sessions cost, and days and totals are what the
	// repo's cost on each day and over each of budget's scales, as of the
	// last reload.
	spent  *spend.Tally
	totals spend.Totals
	days   []spend.Day
	budget config.Budget

	// confirm names the key and goal a first press asked to confirm, such
	// as a sign-off.
	confirm string
	// busyGoal is the goal a background job works on, and log what it has
	// said so far about logGoal, kept after it ends until the human does
	// something else.
	busyGoal, logGoal string
	log               *jobLog
	// busy says what a background job, such as preparing a goal to land, is
	// doing; the jobs confirmed while it runs wait in queued, and start in
	// turn as it ends.
	busy   string
	queued []job

	// health says what keeps the scheduler from starting work, when
	// something does: it isn't running, or every pass fails.
	health string

	// detail is the row opened with enter: its tasks, or one of them.
	detail *detail
	// openReview opens a goal's review, where the window has one, and
	// answering is the goal whose questions the human asked to answer, for
	// the window to open in Next.
	openReview func(goal string)
	answering  string

	width, height int
	// flash and err are what the human's last action came to, kept until
	// they do something else; loadErr is why the last reload failed.
	flash        string
	err, loadErr error
}

// NewStatus loads the status pane.
func NewStatus(ctx context.Context, env Env) *Status {
	s := &Status{
		ctx: ctx, env: env, hunks: map[string]int{}, spent: spend.New(),
		width: 80, height: 24,
	}
	s.reload()
	return s
}

func (s *Status) reload() {
	s.loadErr = nil
	store := s.env.Store
	s.health = health(store)
	s.days = s.spent.Days(store, s.env.now())
	s.totals = spend.Sum(s.days, s.env.now())
	land := ""
	if cfg, err := config.Load(store.Repo(), s.env.Paths); err == nil {
		s.budget, land = cfg.Budget, cfg.Land
	}
	goals, err := store.Goals()
	if err != nil {
		s.loadErr = err
		return
	}
	notes := map[string]int{}
	if pending, err := intake.Pending(intake.Dir(store.Repo())); err == nil {
		for _, in := range pending {
			if in.Source == "review" {
				notes[in.Goal]++
			}
		}
	}
	var rows []goalRow
	// What the human sent comes first, while triage has any of it.
	if g, err := store.Goal(queue.IntakeGoal); err == nil {
		row, err := s.row(store, g)
		if err != nil {
			s.loadErr = err
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
			s.loadErr = err
			continue
		}
		row.notes, row.land = notes[g.Name], land
		rows = append(rows, row)
	}
	s.rows = rows
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
	if g.Name != queue.IntakeGoal {
		if row.description, err = roster.About(store, g); err != nil {
			return row, err
		}
	}
	if err := stage(store, &row); err != nil {
		return row, err
	}
	commits, landing := countTasks(&row, tasks)
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
	if row.landingReview, err = s.toReview(store, g.Name, landing); err != nil {
		return row, err
	}
	slices.Sort(row.activeWork)
	row.activeWork = slices.Compact(row.activeWork)
	row.latest = map[string]string{}
	for _, ws := range row.activeWork {
		name := ws
		if name == "" {
			// Triage and grilling run on no workstream, in planning sessions.
			name = "planning"
		}
		dir := newestSession(store, g.Name, name)
		row.latest[ws] = lastEvent(dir)
		if settling(dir) {
			row.settling = append(row.settling, ws)
		}
	}
	row.cost, row.taskCost = s.goalCost(g.Name)
	if g.State == queue.GoalActive && len(row.waiting) == 0 {
		for _, t := range schedule.Ready(tasks) {
			if !slices.Contains(row.activeWork, t.Workstream) {
				row.ready++
			}
		}
	}
	return row, nil
}

// countTasks counts a goal's tasks by state, and what runs, and returns their
// commits: all of them, and those finishing the goal brought.
func countTasks(row *goalRow, tasks []*queue.Task) (commits, landing []string) {
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
			if (finish.IsLanding(t) || t.Merge != "") && !slices.Contains(landing, sha) {
				landing = append(landing, sha)
			}
		}
	}
	return commits, landing
}

// stage loads what a goal's state waits on: a plan in planning, whether the
// human sent it back, and a done goal's layout.
func stage(store *queue.Store, row *goalRow) error {
	dir := store.GoalDir(row.goal.Name)
	var err error
	switch row.goal.State {
	case queue.GoalPlanning:
		if row.plan, err = plan.Load(dir); err != nil {
			return err
		}
		sent, err := intake.Pending(plan.FeedbackDir(dir))
		if err != nil {
			return err
		}
		row.sentBack = len(sent) > 0
	case queue.GoalDone:
		row.landing, err = finish.Load(dir)
	}
	return err
}

// goalCost adds up what the goal's sessions have cost, in all and for each
// task: a session's cost is shared evenly among its tasks. A session still
// running counts what its runs that ended cost.
func (s *Status) goalCost(goal string) (float64, map[string]float64) {
	total, perTask := 0.0, map[string]float64{}
	for _, c := range s.spent.Goal(s.env.Store, goal) {
		total += c.USD
		for _, t := range c.Tasks {
			perTask[t] += c.USD / float64(len(c.Tasks))
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

// reviewSettlements holds a landing back until the human has reviewed how an
// agent settled its rebase's conflicts. A settlement they rejected is thrown
// away with the layout, which is made again with their comments. It returns
// a note when the goal waits on review.
func reviewSettlements(
	ctx context.Context,
	s *queue.Store,
	paths config.Paths,
	run runner.Runner,
	exe string,
	g *queue.Goal,
	res **finish.Result,
	log io.Writer,
) (string, error) {
	waiting, feedback, err := finish.Settled(ctx, s, g, *res)
	if err != nil {
		return "", err
	}
	if len(feedback) > 0 {
		_, _ = fmt.Fprintln(
			log,
			"You rejected how some conflicts were settled: settling them again",
		)
		if err := finish.SaveFeedback(s.GoalDir(g.Name), feedback); err != nil {
			return "", err
		}
		if err := finish.Discard(s.GoalDir(g.Name)); err != nil {
			return "", err
		}
		if *res, err = prepare(ctx, s, paths, run, exe, g, log); err != nil {
			return "", err
		}
		if waiting, _, err = finish.Settled(ctx, s, g, *res); err != nil {
			return "", err
		}
	}
	if !waiting {
		return "", nil
	}
	_, _ = fmt.Fprintln(log, "An agent settled conflicts: review its changes before the goal lands")
	return fmt.Sprintf(
		"an agent settled %s in rebasing %s onto %s: review what it changed in Next, "+
			"then land it again",
		count(len((*res).Settlements), "conflicted commit"),
		g.Name,
		g.Base,
	), nil
}

// resolveProfile is the profile of the agent settling a landing's
// conflicts: work that needs judgement, but not the most.
const resolveProfile = "mechanical"

// resolveConflict has an agent settle the conflicts rebasing a goal's commit
// left in dir, telling log what it does. It returns how the agent ended, nil
// when it didn't run.
func resolveConflict(
	ctx context.Context,
	run runner.Runner,
	profile config.Profile,
	cfg *config.Config,
	exe string,
	g *queue.Goal,
	dir string,
	c finish.Conflict,
	log io.Writer,
) (*runner.Result, error) {
	prompt := fmt.Sprintf(
		`You are settling a merge conflict. diatom is rebasing the commits of the goal %q onto
%s, which changed the same code while the goal ran. Git applied commit %s, %q, onto %s's
tip and left these files conflicted, with the cherry-pick in progress in your working
directory:

- %s

Resolve every conflict in these files so that both sides survive: nothing %s changed may be
lost, and the commit's own change must still be made. Keep to what this commit changes;
don't bring in what later commits of the goal do. When one side deleted a file the other
changed, decide from what each side meant.

How the goal as a whole was reconciled with %s, as the human reviewed it, is on %s: read
it with `+"`git show %s:<path>`"+` as a guide, not a copy.

Leave no conflict markers. Only edit files: diatom stages and commits the result, and git
commands that change the repository are blocked. End once every file is resolved.%s`,
		g.Title,
		g.Base,
		tui.Short(c.Commit),
		c.Subject,
		g.Base,
		strings.Join(c.Files, "\n- "),
		g.Base,
		g.Base,
		c.Integration,
		c.Integration,
		feedbackNote(c.Feedback),
	)
	spec := runner.Spec{
		Dir:            dir,
		Prompt:         prompt,
		Profile:        profile,
		CommandTimeout: cfg.CommandTimeout,
	}
	if exe != "" {
		spec.Hooks.PreToolUse = exe + " hook pre-tool-use"
	}
	res, err := run.Run(ctx, spec, func(e runner.Event) {
		if e.Summary != "" {
			_, _ = fmt.Fprintln(log, "  "+e.Summary)
		}
	})
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "  the agent %s, $%.2f\n", res.Outcome, res.Usage.CostUSD)
	if res.Outcome != runner.Completed {
		return &res, fmt.Errorf("the agent settling %s ended %s", tui.Short(c.Commit), res.Outcome)
	}
	return &res, nil
}

// recordSession keeps what an agent run outside the scheduler cost, as a
// settled session of the goal's in root, so the goal and the repo's budget
// count it.
func recordSession(root string, at time.Time, profile string, res runner.Result) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	base := at.UTC().Format("20060102T150405Z") + "-landing"
	for n := 0; ; n++ {
		id := base
		if n > 0 {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		dir := filepath.Join(root, id)
		err := os.Mkdir(dir, 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := session.Create(dir, session.Spec{ID: id, Kind: queue.Conflict,
			Profile: profile}); err != nil {
			return err
		}
		if err := session.UpdateState(
			dir,
			func(st *session.State) { st.Settled = true },
		); err != nil {
			return err
		}
		return session.WriteResult(dir, res)
	}
}

// feedbackNote is what the human said of the last settlement, for the agent
// settling it again.
func feedbackNote(feedback string) string {
	if feedback == "" {
		return ""
	}
	return "\n\nThe human rejected how this commit was settled last time, saying:\n\n" + feedback
}

// gateLines is how much of a failing gate's output shows under the notice.
const gateLines = 20

// prepare returns the goal's commits ready to land, building them again
// when the goal's branches have moved since, or they failed the gate. When
// the base changed the goal's own code, an agent settles each conflict git
// leaves in rebasing the commits onto it.
func prepare(
	ctx context.Context,
	s *queue.Store,
	paths config.Paths,
	run runner.Runner,
	exe string,
	g *queue.Goal,
	log io.Writer,
) (*finish.Result, error) {
	res, err := finish.Ready(ctx, s, g)
	if err != nil || res != nil && res.Failing() == nil {
		return res, err
	}
	cfg, err := config.Load(s.Repo(), paths)
	if err != nil {
		return nil, err
	}
	feedback, err := finish.LoadFeedback(s.GoalDir(g.Name))
	if err != nil {
		return nil, err
	}
	opts := finish.Options{
		Gate:     cfg.Gate,
		Timeout:  cfg.GateTimeout,
		Progress: log,
		Feedback: feedback,
	}
	if run != nil {
		profile, err := cfg.Profile(resolveProfile)
		if err != nil {
			return nil, err
		}
		opts.Resolve = func(ctx context.Context, dir string, c finish.Conflict) error {
			res, err := resolveConflict(ctx, run, profile, cfg, exe, g, dir, c, log)
			if res != nil {
				if rerr := recordSession(s.SessionsDir(g.Name), time.Now(), resolveProfile,
					*res); rerr != nil {
					_, _ = fmt.Fprintln(
						log,
						"  recording what the agent cost failed: "+rerr.Error(),
					)
				}
			}
			return err
		}
	}
	return finish.Build(ctx, s, g, opts)
}

// jobDone ends a background job, such as preparing a goal to land, and
// starts the next one waiting.
func (s *Status) jobDone(msg jobMsg) tea.Cmd {
	s.reload()
	done := s.busyGoal
	s.busy, s.busyGoal, s.flash, s.err = "", "", msg.flash, msg.err
	if done != "" && s.flash != "" {
		s.flash = done + ": " + s.flash
	}
	if msg.err != nil && done != "" {
		s.err = fmt.Errorf("%s: %w", done, msg.err)
	}
	if len(s.queued) == 0 {
		return nil
	}
	next := s.queued[0]
	s.queued = s.queued[1:]
	return s.start(next)
}

// act does what one of a goal's keys does, and reports whether key is one.
func (s *Status) act(row *goalRow, key string) (tea.Cmd, bool) {
	switch key {
	case "r":
		if s.openReview == nil || row.intake {
			return nil, false
		}
		s.openReview(row.goal.Name)
		return nil, true
	case "a":
		if row.questions == 0 && !planReady(row) {
			return nil, false
		}
		s.answering = row.goal.Name
		return nil, true
	case "p":
		s.toggleParked(row)
	case "d", "F", "P":
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
		if planReady(r) {
			a = append(a, action{"a", fmt.Sprintf("Review the plan: %d workstreams, %d tasks",
				len(r.plan.Workstreams), len(r.plan.Tasks))})
		}
	case queue.GoalActive:
		left := r.counts[queue.Pending] + r.counts[queue.Active] + r.counts[queue.Blocked]
		if left == 0 && r.toReview == 0 {
			a = append(append(a, landActions(r)...), action{"d", "Mark it done, to land later"})
		}
		a = append(a, action{"p", "Park it: start nothing new"})
	case queue.GoalParked:
		a = append(a, action{"p", "Resume it"})
	case queue.GoalDone:
		if l := r.landing; l == nil || l.Landing == nil || l.Landing.How == "" {
			a = append(a, landActions(r)...)
		}
	}
	return a
}

// landActions are the ways the goal may land, as its repo's config allows.
func landActions(r *goalRow) []action {
	var a []action
	if r.land != config.LandPRs {
		a = append(a, action{"P", "Merge it into " + r.goal.Base})
	}
	if r.land != config.LandMerge {
		a = append(a, action{"F", "Open its stacked pull requests"})
	}
	return a
}

// landsBy reports whether the goal may land by key, P or F, and says why not
// when it may not.
func (s *Status) landsBy(row *goalRow, key string) bool {
	switch {
	case key == "P" && row.land == config.LandPRs:
		s.flash = row.goal.Name + "'s repo lands goals as pull requests: F opens them"
	case key == "F" && row.land == config.LandMerge:
		s.flash = row.goal.Name + "'s repo merges goals straight in: P merges it into " + row.goal.Base
	default:
		return true
	}
	s.confirm = ""
	return false
}

// planReady reports whether the goal's plan waits for the human to approve
// or comment on it.
func planReady(r *goalRow) bool {
	return r.goal.State == queue.GoalPlanning && r.plan != nil && !r.sentBack
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

// job is a goal's landing the human confirmed: its key, and what it does.
type job struct {
	repo, goal, key, what string
}

// landing reports whether a job lands the goal, or waits to.
func (s *Status) landing(goal string) bool {
	return s.busyGoal == goal || s.queuedAt(goal) >= 0
}

// queuedAt is where the goal's job waits in the queue, -1 when none does.
func (s *Status) queuedAt(goal string) int {
	for i, j := range s.queued {
		if j.goal == goal {
			return i
		}
	}
	return -1
}

// finishKey handles the keys that end and land a goal (ADR 0003), each
// confirmed with a second press: P merges it into its base branch, F opens
// its pull requests, and d marks it done, to land later. Landing an active
// goal marks it done first. Preparing it runs the gate, so the job runs in
// the background, telling its log each step.
func (s *Status) finishKey(row *goalRow, key string) tea.Cmd {
	if s.landing(row.goal.Name) {
		s.flash = row.goal.Name + " is landing already"
		return nil
	}
	g, name := row.goal, row.repo+"/"+row.goal.Name
	var prompt, what string
	switch key {
	case "d":
		if g.State == queue.GoalDone {
			var how []string
			for _, a := range landActions(row) {
				how = append(how, a.key+" "+strings.ToLower(a.label[:1])+a.label[1:])
			}
			s.flash = g.Name + " is done already: " + strings.Join(how, ", ")
			return nil
		}
		prompt = fmt.Sprintf("press d again to mark %s done, ready to land", g.Name)
		what = "marking it done"
	default:
		if !s.landsBy(row, key) {
			return nil
		}
		if g.State != queue.GoalDone && g.State != queue.GoalActive {
			s.flash = fmt.Sprintf("%s is %s: only an active or done goal lands", g.Name, g.State)
			return nil
		}
		prompt = fmt.Sprintf("press F again to push %s and open its pull requests", g.Name)
		what = "opening its pull requests"
		if key == "P" {
			prompt = fmt.Sprintf("press P again to merge %s into %s", g.Name, g.Base)
			what = "merging it into " + g.Base
		}
	}
	if !s.confirmed(key, name, prompt) {
		return nil
	}
	j := job{repo: row.repo, goal: g.Name, key: key, what: what}
	if s.busy != "" {
		// One job runs at a time, since each takes the repo's git and gate.
		s.queued = append(s.queued, j)
		s.flash = fmt.Sprintf("%s lands after %s", g.Name, s.busyGoal)
		return nil
	}
	return s.start(j)
}

// start runs a job in the background, on the goal as it stands now.
func (s *Status) start(j job) tea.Cmd {
	store := queue.Open(j.repo)
	g, err := store.Goal(j.goal)
	if err != nil {
		return func() tea.Msg { return jobMsg{err: err} }
	}
	s.busy, s.busyGoal, s.logGoal, s.log = j.what, g.Name, g.Name, &jobLog{}
	paths, run, exe, ctx, log := s.env.Paths, s.env.Runner, s.env.Exe, s.ctx, s.log
	return tea.Batch(func() tea.Msg {
		flash, err := runFinish(ctx, store, paths, run, exe, g, j.key, log)
		return jobMsg{flash: flash, err: err}
	}, jobTick())
}

// jobTickEvery is how often the window draws a running job's log again.
const jobTickEvery = 250 * time.Millisecond

// jobTickMsg draws a running job's log again.
type jobTickMsg struct{}

func jobTick() tea.Cmd {
	return tea.Tick(jobTickEvery, func(time.Time) tea.Msg { return jobTickMsg{} })
}

// jobLog is what a background job has said so far: written from its
// goroutine, read as the window draws.
type jobLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *jobLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(b)
}

// tail is the log's last n lines.
func (l *jobLog) tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := strings.Split(strings.TrimRight(l.text.String(), "\n"), "\n")
	return lines[max(len(lines)-n, 0):]
}

// runFinish does what finishKey confirmed, telling log each step. The goal
// first catches up with its base branch: when that conflicts, an agent merges
// it in, and the goal is active again until the resolution is reviewed.
func runFinish(
	ctx context.Context,
	s *queue.Store,
	paths config.Paths,
	run runner.Runner,
	exe string,
	g *queue.Goal,
	key string,
	log io.Writer,
) (string, error) {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(log, format+"\n", args...) }
	remote := "origin"
	if old, err := finish.Load(s.GoalDir(g.Name)); err == nil && old != nil && old.Landing != nil &&
		old.Landing.Remote != "" {
		remote = old.Landing.Remote
	}
	say("Fetching %s/%s, and merging what it gained into the goal", remote, g.Base)
	up, err := finish.CatchUp(ctx, s, g, remote, time.Now())
	if err != nil {
		return "", err
	}
	if !up {
		say("%s changed the goal's own code: an agent is merging it in", g.Base)
		return fmt.Sprintf("%s has moved on and conflicts with %s: an agent is merging it in, and "+
			"its resolution comes back for review before the goal lands", g.Base, g.Name), nil
	}
	wasActive := g.State == queue.GoalActive
	if wasActive {
		say("Marking it done")
		if err := finish.MarkDone(ctx, s, g, false); err != nil {
			return "", err
		}
	}
	res, err := prepare(ctx, s, paths, run, exe, g, log)
	if err != nil {
		if wasActive {
			// Nothing is ready to land, so the goal isn't done after all.
			g.State = queue.GoalActive
			err = errors.Join(err, s.SaveGoal(g))
		}
		return "", err
	}
	if note, err := reviewSettlements(
		ctx,
		s,
		paths,
		run,
		exe,
		g,
		&res,
		log,
	); note != "" ||
		err != nil {
		if wasActive || g.State == queue.GoalDone {
			g.State = queue.GoalActive
			err = errors.Join(err, s.SaveGoal(g))
		}
		return note, err
	}
	if failed := res.Failing(); failed != nil {
		// Landing never forces past the gate: an agent makes it pass.
		say("The gate fails, so it doesn't land: an agent is making it pass")
		if err := finish.RepairGate(s, g, failed, time.Now()); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s fails the gate on %s's tip, so it didn't land: an agent is making "+
				"it pass, and it comes back to Next once it does", g.Name, g.Base),
			fmt.Errorf("the gate on %s:\n%s", failed.Branch, gate.Tail(failed.Output, gateLines))
	}
	if err := finish.SaveFeedback(s.GoalDir(g.Name), nil); err != nil {
		return "", err
	}
	switch key {
	case "F":
		say("Pushing its branches and opening its pull requests")
		urls, err := finish.Land(ctx, s, g, res, finish.PRs, remote, finish.RunGH)
		return fmt.Sprintf("opened %s", strings.Join(urls, " ")), err
	case "P":
		say("Merging it into %s/%s", remote, g.Base)
		if _, err := finish.Land(ctx, s, g, res, finish.Push, remote, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("merged %s into %s on %s, and %s", g.Name, g.Base, remote,
			finish.FollowLocal(ctx, s, g, remote, res.Tip())), nil
	}
	return fmt.Sprintf("%s is done, ready to land as %s", g.Name,
		count(len(res.Stack), "pull request")), nil
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

// health says what keeps the scheduler from starting work, or "" when
// nothing does.
func health(store *queue.Store) string {
	if _, err := store.Scheduler(); errors.Is(err, queue.ErrNotRunning) {
		return "no scheduler: reopen diatom to start work"
	}
	st, err := store.Stuck()
	if err != nil || st == nil {
		return ""
	}
	return fmt.Sprintf("stuck since %s: %s", st.Since.Local().Format("15:04"), st.Error)
}

// foot is what the last key and the background job said.
func (s *Status) foot() []string {
	var foot []string
	for _, err := range []error{s.loadErr, s.err} {
		if err != nil {
			for l := range strings.SplitSeq(err.Error(), "\n") {
				foot = append(foot, tui.Color(l, tui.Red))
			}
		}
	}
	if s.flash != "" {
		foot = append(foot, tui.Color(s.flash, tui.Cyan))
	}
	if s.busy != "" {
		foot = append(foot, tui.Color(s.busyGoal+": "+s.busy+"…", tui.Yellow))
	}
	for _, j := range s.queued {
		foot = append(foot, tui.Dim(j.goal+": "+j.what+", after "+s.busyGoal))
	}
	return foot
}

// scroll shows room lines, moved from *top just enough to show the lines
// from first to last, or first when they don't all fit. Lines wider than w
// are wrapped first, under where their text starts.
func scroll(lines []string, first, last int, top *int, room, w int) string {
	lines, first, last = wrapLines(lines, w, first, last)
	if last >= *top+room {
		*top = last - room + 1
	}
	*top = min(*top, first)
	*top = max(min(*top, len(lines)-room), 0)
	return strings.Join(lines[*top:min(*top+room, len(lines))], "\n")
}

// renderRow renders one goal and what is running for it, at the head of its
// page.
func (s *Status) renderRow(b *strings.Builder, r goalRow) {
	mark := "  "
	if r.intake {
		cost := ""
		if r.cost > 0 {
			cost = tui.Dim(fmt.Sprintf(" · $%.2f", r.cost))
		}
		fmt.Fprintf(b, "%s  %s %s%s\n", mark, tui.Bold("intake"), intakeLine(r), cost)
		s.renderActive(b, r)
		return
	}
	fmt.Fprintf(b, "%s  %s %s\n", mark, tui.Bold(r.goal.Name), goalState(r))
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
			name = "planning"
		}
		fmt.Fprintf(b, "      %s %s\n", tui.Color("▶ "+name, tui.Green), tui.Dim(r.latest[ws]))
	}
}

// landingLine says how far a done goal is on its way upstream.
func landingLine(r goalRow) string {
	line := finish.Summary(r.goal, r.landing)
	switch l := r.landing; {
	case l == nil:
		return tui.Color(line, tui.Yellow)
	case l.Landing == nil || l.Landing.How == "":
		return tui.Color(line+" · P merge into "+r.goal.Base+" · F open PRs", tui.Green)
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
		return tui.Color(fmt.Sprintf("plan ready to approve: %d workstreams, %d tasks",
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
	case left == 0 && r.counts[queue.Done] > 0 && (r.toReview > 0 || r.notes > 0):
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

// newestSession is the directory of a workstream's latest session, "" for
// none.
func newestSession(store *queue.Store, goal, ws string) string {
	dirs, err := filepath.Glob(filepath.Join(store.SessionsDir(goal), "*-"+ws))
	if err != nil || len(dirs) == 0 {
		return ""
	}
	slices.Sort(dirs)
	return dirs[len(dirs)-1]
}

// settling reports whether the session in dir has an agent that ended and
// work diatom hasn't settled yet: it is gating, committing and merging.
func settling(dir string) bool {
	if dir == "" {
		return false
	}
	if _, ended := session.Ended(dir); !ended {
		return false
	}
	st, err := session.LoadState(dir)
	return err == nil && !st.Settled
}

// lastEvent is the latest step of the running session in dir.
func lastEvent(dir string) string {
	if dir == "" {
		return ""
	}
	events, err := session.ReadEvents(dir)
	if err != nil {
		return ""
	}
	st := steps(events)
	if len(st) == 0 {
		return ""
	}
	return oneLine(st[len(st)-1].summary, 90)
}

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
