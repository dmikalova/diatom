// Package harness is diatom's scheduler: the one headless process per repo
// that owns the repo's queue, git operations and agent sessions (ADR 0007).
// Each pass of its loop turns new intake into triage tasks, applies answered
// questions and review decisions, asks schedule for the batches to start, and
// runs each batch in its workstream's worktree.
package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/hook"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/spend"
)

// Harness runs the scheduler loop.
type Harness struct {
	// Paths locate the config walk-up's stopping points.
	Paths config.Paths
	// Root is the repo the scheduler works in, one per scheduler (ADR 0007).
	Root string
	// Runner runs agent sessions.
	Runner runner.Runner
	// Exe is the diatom binary the agent's hooks and task tool run.
	Exe string
	// Gate runs a gate command; nil uses gate.Run.
	Gate hook.GateRunner
	// Poll is how often the loop looks for new work.
	Poll time.Duration
	Log  *slog.Logger
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// GH runs the GitHub CLI to watch done goals land; nil runs gh.
	GH finish.GH

	// gitMu holds a *sync.Mutex per repo, serializing the git operations
	// batches share: creating branches and worktrees, and moving the
	// integration branch.
	gitMu sync.Map
	// autoApproved holds the commits autoApprove has looked at, each with
	// the patterns it used: a commit's hunks never change.
	autoApproved sync.Map
	// spent adds up what the repo's sessions cost, for its budget, and
	// spentNote is the budget last found spent, "" for none.
	spent     *spend.Tally
	spentNote string
}

// lockRepo takes the repo's git lock and returns its unlock.
func (h *Harness) lockRepo(repo string) func() {
	m, _ := h.gitMu.LoadOrStore(repo, &sync.Mutex{})
	mu, ok := m.(*sync.Mutex)
	if !ok {
		panic("harness: gitMu holds a non-mutex")
	}
	mu.Lock()
	return mu.Unlock
}

// cooldown is how long a workstream waits after a batch that failed outright.
const cooldown = time.Minute

// runnable are the task kinds that run in a workstream's worktree; triage and
// grilling run as planning sessions instead.
var runnable = map[queue.Kind]bool{
	queue.GateRepair: true,
	queue.Conflict:   true,
	queue.Revision:   true,
	queue.Planned:    true,
}

// Run runs the loop until stop is done, then waits for the running sessions
// to finish: a new task never interrupts a running session (ADR 0004).
// Cancelling kill suspends the running sessions instead of waiting for them;
// the next Run resumes each where it stopped.
func (h *Harness) Run(stop, kill context.Context) error {
	resumes, err := h.Recover(kill)
	if err != nil {
		return err
	}
	p := &slots{
		running: map[schedule.Running]bool{},
		cooling: map[schedule.Running]time.Time{},
		wake:    make(chan struct{}, 1),
		now:     h.now,
	}
	defer p.wg.Wait()
	// Sessions stopped by the last scheduler carry on first, in the slots
	// they had.
	for _, r := range resumes {
		key := schedule.Running{Repo: r.Repo, Goal: r.Spec.Goal, Workstream: r.Spec.Workstream}
		p.start(key, func() error {
			if err := h.Resume(kill, r); err != nil {
				h.log().Error("resuming a session failed", "session", r.Spec.ID, "err", err)
			}
			return nil
		})
	}
	tick := time.NewTicker(h.poll())
	defer tick.Stop()
	for {
		batches, repos, err := h.plan(kill, p.busy())
		if err != nil && kill.Err() == nil {
			h.log().Error("planning failed", "err", err)
		}
		if kill.Err() == nil {
			// The status pane shows it: until it clears, nothing starts.
			if err := queue.Open(h.Root).SetStuck(errString(err), h.now()); err != nil {
				h.log().Warn("recording the planning error failed", "err", err)
			}
		}
		for _, b := range batches {
			key := schedule.Running{Repo: b.Repo, Goal: b.Goal, Workstream: b.Workstream}
			p.start(key, func() error {
				err := h.RunBatch(kill, repos[b.Repo], b)
				if err != nil {
					h.log().
						Error("batch failed", "repo", b.Repo, "goal", b.Goal, "workstream", b.Workstream,
							"retryIn", cooldown, "err", err)
				}
				return err
			})
		}

		select {
		case <-stop.Done():
			h.log().Info("stopping; waiting for running sessions")
			return nil
		case <-p.wake:
		case <-tick.C:
		}
	}
}

// slots tracks the workstreams with a session running, and those cooling
// down after a batch that failed outright, such as when the agent could not
// start, until they may be tried again.
type slots struct {
	mu      sync.Mutex
	running map[schedule.Running]bool
	cooling map[schedule.Running]time.Time
	wg      sync.WaitGroup
	// wake is signalled when a session ends, so its slot is filled at once.
	wake chan struct{}
	now  func() time.Time
}

// start runs fn in key's slot. An error puts the slot to cool down.
func (p *slots) start(key schedule.Running, fn func() error) {
	p.mu.Lock()
	p.running[key] = true
	p.mu.Unlock()
	p.wg.Go(func() {
		err := fn()
		p.mu.Lock()
		delete(p.running, key)
		if err != nil {
			p.cooling[key] = p.now().Add(cooldown)
		}
		p.mu.Unlock()
		select {
		case p.wake <- struct{}{}:
		default:
		}
	})
}

// busy lists the slots no new batch may take.
func (p *slots) busy() []schedule.Running {
	p.mu.Lock()
	defer p.mu.Unlock()
	busy := make([]schedule.Running, 0, len(p.running)+len(p.cooling))
	for r := range p.running {
		busy = append(busy, r)
	}
	for r, until := range p.cooling {
		if p.now().Before(until) {
			busy = append(busy, r)
		} else {
			delete(p.cooling, r)
		}
	}
	return busy
}

// Repo is one repo's store and merged config.
type Repo struct {
	Store  *queue.Store
	Config *config.Config
}

// plan gathers the repo's ready work and returns the batches to start.
func (h *Harness) plan(
	ctx context.Context,
	busy []schedule.Running,
) ([]schedule.Batch, map[string]Repo, error) {
	repo, goals, err := h.load(ctx, h.Root)
	if repo.Config == nil {
		return nil, nil, err
	}
	if h.overBudget(repo) {
		return nil, map[string]Repo{h.Root: repo}, err
	}
	lim := schedule.Limits{Repos: map[string]schedule.RepoLimits{h.Root: {
		Sessions: repo.Config.MaxSessions,
		Batch:    repo.Config.MaxBatch,
	}}, Covers: repo.Config.Covers}
	return schedule.Next(goals, busy, lim), map[string]Repo{h.Root: repo}, err
}

// overBudget reports whether one of the repo's budgets is spent, when no new
// session may start: the ones running carry on. It logs each change.
func (h *Harness) overBudget(repo Repo) bool {
	if h.spent == nil {
		h.spent = spend.New()
	}
	b := repo.Config.Budget
	over := ""
	if b != (config.Budget{}) {
		over = h.spent.Repo(repo.Store, h.now()).Over(b)
	}
	switch {
	case over == h.spentNote:
	case over != "":
		h.log().Warn("the budget is spent: no new session starts until spending falls under it",
			"budget", over)
	default:
		h.log().Info("spending is under the budget again: sessions start")
	}
	h.spentNote = over
	return over != ""
}

// load reads one repo's active goals and their ready tasks, applying answered
// questions first so their tasks are ready again.
func (h *Harness) load(ctx context.Context, path string) (Repo, []*schedule.Goal, error) {
	cfg, err := config.Load(path, h.Paths)
	if err != nil {
		return Repo{}, nil, err
	}
	repo := Repo{Store: queue.Open(path), Config: cfg}
	if err := h.applyIntake(ctx, repo.Store); err != nil {
		return repo, nil, err
	}
	all, err := repo.Store.Goals()
	if err != nil {
		return repo, nil, err
	}
	h.watchDone(ctx, repo.Store, all)
	// Triage runs beside the goals, in the goal that holds it.
	if g, err := repo.Store.Goal(queue.IntakeGoal); err == nil {
		all = append([]*queue.Goal{g}, all...)
	}
	var goals []*schedule.Goal
	// A goal that fails to load waits, and says why; the others go on.
	var errs []error
	for _, g := range all {
		if g.State != queue.GoalActive && g.State != queue.GoalPlanning {
			continue
		}
		sg, err := h.loadGoal(ctx, repo, g)
		if err != nil {
			errs = append(errs, fmt.Errorf("goal %s: %w", g.Name, err))
			continue
		}
		sg.Repo, sg.Name, sg.Created = path, g.Name, g.Created
		goals = append(goals, sg)
	}
	return repo, goals, errors.Join(append(errs, ctx.Err())...)
}

// loadGoal brings in what the human decided for a goal since the last look,
// answers, reviews and feedback, and returns its tasks ready to run, with
// the planned work that can follow them in the same session.
func (h *Harness) loadGoal(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
) (*schedule.Goal, error) {
	s := repo.Store
	if err := h.applyAnswers(s, g.Name); err != nil {
		return nil, err
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return nil, err
	}
	if waiting := s.Waiting(g); len(waiting) > 0 {
		// Nothing starts, grilling included, until the goals it waits for
		// have landed: its branch then starts from them (ADR 0003).
		return &schedule.Goal{}, nil
	}
	if err := h.catchUpAfter(ctx, repo, g); err != nil {
		return nil, err
	}
	switch {
	case g.Name == queue.IntakeGoal:
	case g.State == queue.GoalActive:
		if err = h.autoApprove(ctx, repo, g.Name, tasks); err == nil {
			err = h.applyReviews(ctx, s, g.Name)
		}
	default:
		err = h.applyFeedback(s, g.Name, tasks)
	}
	if err != nil {
		return nil, err
	}
	if tasks, err = s.Tasks(g.Name); err != nil {
		return nil, err
	}
	var ready []*queue.Task
	for _, t := range schedule.Ready(tasks) {
		switch {
		case g.State == queue.GoalPlanning:
			// Nothing but grilling runs before the plan is signed off.
			if t.Kind == queue.Grilling {
				ready = append(ready, t)
			}
		case planningKind(t.Kind) || runnable[t.Kind] && t.Workstream != "":
			ready = append(ready, t)
		}
	}
	sg := &schedule.Goal{Ready: ready, Unfinished: map[string]bool{}}
	for _, t := range tasks {
		if t.State != queue.Done {
			sg.Unfinished[t.ID] = true
		}
		if g.State == queue.GoalActive && t.State == queue.Pending && runnable[t.Kind] &&
			t.Workstream != "" && !slices.Contains(ready, t) {
			sg.Later = append(sg.Later, t)
		}
	}
	return sg, nil
}

// catchUpAfter merges into a goal that waited what the goals it waited for
// landed, once for each, as it stops waiting. A branch made before they
// landed, such as by the grilling that found the goal must wait, would
// otherwise go on without them. A merge that conflicts goes to an agent, as
// catching up before landing does.
func (h *Harness) catchUpAfter(ctx context.Context, repo Repo, g *queue.Goal) error {
	var fresh []string
	for _, a := range g.After {
		if !slices.Contains(g.CaughtUp, a) {
			fresh = append(fresh, a)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	main := git.Repo{Dir: repo.Store.Repo()}
	if main.BranchExists(ctx, g.IntegrationBranch()) {
		unlock := h.lockRepo(main.Dir)
		up, err := finish.CatchUp(ctx, repo.Store, g, "origin", h.now())
		unlock()
		if err != nil {
			return fmt.Errorf(
				"merging %s's landed work into it: %w",
				strings.Join(fresh, ", "),
				err,
			)
		}
		h.log().Info("a goal that waited took in what it waited for", "goal", g.Name,
			"after", fresh, "clean", up)
	}
	g.CaughtUp = append(g.CaughtUp, fresh...)
	return repo.Store.SaveGoal(g)
}

// applyAnswers adds each answered question's answer to its task, makes the
// task ready again, and closes the question (ADR 0009).
func (h *Harness) applyAnswers(s *queue.Store, goal string) error {
	open, err := s.Questions(goal, queue.QuestionOpen)
	if err != nil {
		return err
	}
	for _, q := range open {
		if q.Answer == "" {
			continue
		}
		t, err := s.Task(goal, q.Task)
		if err != nil {
			return err
		}
		asked, answer := "Question ", "Answer "
		if q.Manual {
			asked, answer = "Manual steps ", "Done by hand "
		}
		t.Body = appendSection(t.Body, asked+q.ID, q.Text)
		t.Body = appendSection(t.Body, answer+q.ID, q.Answer)
		if t.State == queue.Blocked && !stillBlocked(t.ID, q.ID, open) {
			if err := s.Move(goal, t, queue.Pending); err != nil {
				return err
			}
		} else if err := s.SaveTask(goal, t); err != nil {
			return err
		}
		if err := s.CloseQuestion(goal, q); err != nil {
			return err
		}
	}
	return nil
}

// stillBlocked reports whether task has another open question besides the
// one being answered, which keeps it blocked.
func stillBlocked(task, answered string, open []*queue.Question) bool {
	for _, q := range open {
		if q.Task == task && q.ID != answered && q.Answer == "" {
			return true
		}
	}
	return false
}

// goalStart is where a goal's integration branch starts: its base branch,
// or the upstream branch that one follows when it is ahead, as it is once a
// goal this one waited for has landed there and before the human has pulled.
func goalStart(ctx context.Context, main git.Repo, g *queue.Goal) string {
	return finish.BaseTip(ctx, main, g)
}

// watchTimeout bounds one check of a done goal's landing upstream.
const watchTimeout = 30 * time.Second

// watchDone watches each done goal land.
func (h *Harness) watchDone(ctx context.Context, s *queue.Store, goals []*queue.Goal) {
	for _, g := range goals {
		if g.State == queue.GoalDone {
			h.watchLanding(ctx, s, g)
		}
	}
}

// watchLanding finishes a done goal once it has landed upstream: merged into
// its base branch there, with the checks passing (ADR 0003). A failed check
// is kept on the goal's layout for the window, and tried again later.
func (h *Harness) watchLanding(ctx context.Context, s *queue.Store, g *queue.Goal) {
	gh := h.GH
	if gh == nil {
		gh = finish.RunGH
	}
	ctx, cancel := context.WithTimeout(ctx, watchTimeout)
	defer cancel()
	finished, err := finish.Watch(ctx, s, g, gh, h.now())
	if err != nil {
		h.log().
			Warn("checking a done goal upstream failed", "repo", s.Repo(), "goal", g.Name, "err", err)
	}
	if !finished {
		return
	}
	h.log().Info("goal finished: merged upstream and passing", "repo", s.Repo(), "goal", g.Name)
	if err := finish.RemoveWorktrees(ctx, s, g); err != nil {
		h.log().Warn("removing a finished goal's worktrees failed", "goal", g.Name, "err", err)
	}
}

func (h *Harness) poll() time.Duration {
	if h.Poll > 0 {
		return h.Poll
	}
	return 5 * time.Second
}

func (h *Harness) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *Harness) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Harness) runGate(
	ctx context.Context,
	dir string,
	cfg *config.Config,
) (gate.Result, error) {
	if h.Gate != nil {
		return gate.Within(cfg.GateTimeout, h.Gate)(ctx, dir, cfg.Gate)
	}
	return gate.Serial(gate.Within(cfg.GateTimeout, gate.Run))(ctx, dir, cfg.Gate)
}
