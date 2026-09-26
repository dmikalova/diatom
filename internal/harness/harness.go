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

// runnable are the task kinds the loop runs so far. Triage and grilling need
// a planning session outside any worktree, which comes later.
var runnable = map[queue.Kind]bool{
	queue.GateRepair: true,
	queue.Conflict:   true,
	queue.Revision:   true,
	queue.Planned:    true,
}

// Run runs the loop until stop is done, then waits for the running sessions
// to finish: a new task never interrupts a running session (ADR 0004), and
// neither does stopping. Cancelling kill ends the sessions too.
func (h *Harness) Run(stop, kill context.Context) error {
	if err := h.Recover(kill); err != nil {
		return err
	}
	var (
		mu      sync.Mutex
		running = map[schedule.Running]bool{}
		// cooling holds workstreams whose last batch failed outright, such
		// as when the agent could not start, until they may be tried again.
		cooling = map[schedule.Running]time.Time{}
		wg      sync.WaitGroup
		wake    = make(chan struct{}, 1)
	)
	defer wg.Wait()
	tick := time.NewTicker(h.poll())
	defer tick.Stop()
	for {
		mu.Lock()
		busy := make([]schedule.Running, 0, len(running)+len(cooling))
		for r := range running {
			busy = append(busy, r)
		}
		for r, until := range cooling {
			if h.now().Before(until) {
				busy = append(busy, r)
			} else {
				delete(cooling, r)
			}
		}
		mu.Unlock()

		batches, repos, err := h.plan(kill, busy)
		if err != nil {
			h.log().Error("planning failed", "err", err)
		}
		for _, b := range batches {
			key := schedule.Running{Repo: b.Repo, Goal: b.Goal, Workstream: b.Workstream}
			mu.Lock()
			running[key] = true
			mu.Unlock()
			wg.Go(func() {
				err := h.RunBatch(kill, repos[b.Repo], b)
				if err != nil {
					h.log().
						Error("batch failed", "repo", b.Repo, "goal", b.Goal, "workstream", b.Workstream,
							"retryIn", cooldown, "err", err)
				}
				mu.Lock()
				delete(running, key)
				if err != nil {
					cooling[key] = h.now().Add(cooldown)
				}
				mu.Unlock()
				select {
				case wake <- struct{}{}:
				default:
				}
			})
		}

		select {
		case <-stop.Done():
			h.log().Info("stopping; waiting for running sessions")
			return nil
		case <-wake:
		case <-tick.C:
		}
	}
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
	all, err := repo.Store.Goals()
	if err != nil {
		return repo, nil, err
	}
	var goals []*schedule.Goal
	for _, g := range all {
		if g.State != queue.GoalActive {
			continue
		}
		if err := h.applyAnswers(repo.Store, g.Name); err != nil {
			return repo, nil, err
		}
		tasks, err := repo.Store.Tasks(g.Name)
		if err != nil {
			return repo, nil, err
		}
		var ready []*queue.Task
		for _, t := range schedule.Ready(tasks) {
			if runnable[t.Kind] && t.Workstream != "" {
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

// Recover puts the tasks a crashed or killed scheduler left active back to
// pending. Their worktrees keep whatever the session had written, and the
// next session carries on from there.
func (h *Harness) Recover(ctx context.Context) error {
	paths, err := h.Repos()
	if err != nil {
		return err
	}
	for _, path := range paths {
		s := queue.Open(path)
		goals, err := s.Goals()
		if err != nil {
			return err
		}
		for _, g := range goals {
			tasks, err := s.Tasks(g.Name)
			if err != nil {
				return err
			}
			for _, t := range tasks {
				if t.State != queue.Active {
					continue
				}
				h.log().
					Warn("recovering task left active", "repo", path, "goal", g.Name, "task", t.ID)
				if err := s.Move(g.Name, t, queue.Pending); err != nil {
					return err
				}
			}
		}
	}
	return ctx.Err()
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
