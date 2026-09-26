// Package harness is diatom's scheduler: the one headless process per machine
// that owns every queue, git operation and agent session (ADR 0007). Each
// pass of its loop reads the queues of every known repo, applies answered
// questions, asks schedule for the batches to start, and runs each batch in
// its workstream's worktree.
package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/hook"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
)

// Harness runs the scheduler loop.
type Harness struct {
	// Paths locate the config walk-up's stopping points.
	Paths config.Paths
	// Repos lists the known repos.
	Repos func() ([]string, error)
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

// plan gathers the ready work across every known repo and returns the
// batches to start.
func (h *Harness) plan(
	ctx context.Context,
	busy []schedule.Running,
) ([]schedule.Batch, map[string]Repo, error) {
	paths, err := h.Repos()
	if err != nil {
		return nil, nil, err
	}
	home, err := config.LoadHome(h.Paths)
	if err != nil {
		return nil, nil, err
	}
	lim := schedule.Limits{Machine: home.MachineSessions, Repos: map[string]schedule.RepoLimits{}}
	repos := map[string]Repo{}
	var goals []*schedule.Goal
	var errs []error
	for _, path := range paths {
		repo, gs, err := h.load(ctx, path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		repos[path] = repo
		lim.Repos[path] = schedule.RepoLimits{
			Sessions: repo.Config.MaxSessions,
			Batch:    repo.Config.MaxBatch,
		}
		goals = append(goals, gs...)
	}
	return schedule.Next(goals, busy, lim), repos, errors.Join(errs...)
}

// load reads one repo's active goals and their ready tasks, applying answered
// questions first so their tasks are ready again.
func (h *Harness) load(ctx context.Context, path string) (Repo, []*schedule.Goal, error) {
	cfg, err := config.Load(path, h.Paths)
	if err != nil {
		return Repo{}, nil, err
	}
	repo := Repo{Store: queue.Open(path), Config: cfg}
	if err := h.applyRepoIntake(ctx, repo.Store); err != nil {
		return repo, nil, err
	}
	all, err := repo.Store.Goals()
	if err != nil {
		return repo, nil, err
	}
	var goals []*schedule.Goal
	h.watchDone(ctx, repo.Store, all)
	for _, g := range all {
		if g.State != queue.GoalActive && g.State != queue.GoalPlanning {
			continue
		}
		if err := h.applyAnswers(repo.Store, g.Name); err != nil {
			return repo, nil, err
		}
		tasks, err := repo.Store.Tasks(g.Name)
		if err != nil {
			return repo, nil, err
		}
		if g.State == queue.GoalActive {
			err = errors.Join(
				h.applyReviews(ctx, repo.Store, g.Name),
				h.applyGoalIntake(repo.Store, g.Name, tasks),
			)
		} else {
			err = h.applyPlanningIntake(repo.Store, g.Name, tasks)
		}
		if err != nil {
			return repo, nil, err
		}
		if tasks, err = repo.Store.Tasks(g.Name); err != nil {
			return repo, nil, err
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
		goals = append(
			goals,
			&schedule.Goal{
				Repo:    path,
				Name:    g.Name,
				Pinned:  g.Pinned,
				Created: g.Created,
				Ready:   ready,
			},
		)
	}
	return repo, goals, ctx.Err()
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
		t.Body = appendSection(t.Body, "Question "+q.ID, q.Text)
		t.Body = appendSection(t.Body, "Answer "+q.ID, q.Answer)
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
// is kept on the goal's layout for the panes, and tried again later.
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

func (h *Harness) runGate(ctx context.Context, dir, command string) (gate.Result, error) {
	if h.Gate != nil {
		return h.Gate(ctx, dir, command)
	}
	return gate.Run(ctx, dir, command)
}
