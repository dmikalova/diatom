package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

// maxResumes is how often one session is resumed before its tasks start
// over in a new one, so a session that keeps stopping the scheduler can't
// stop it forever.
const maxResumes = 3

// Resumable is a session the last scheduler stopped midway.
type Resumable struct {
	Repo string
	Dir  string
	Spec session.Spec
	// AgentSession is the agent's session to carry on; empty when the agent
	// had already finished and only the gate and commit are left.
	AgentSession string
}

// Recover finds the sessions a stopped scheduler left unsettled, to resume.
// A task left active that no such session holds, because its session never
// got as far as starting its agent, goes back to pending, as do the tasks of
// a session resumed too often; their worktrees keep whatever was written,
// and the next session carries on from there.
func (h *Harness) Recover(ctx context.Context) ([]Resumable, error) {
	var resumes []Resumable
	for _, root := range h.Roots {
		rs, err := h.recoverRepo(root)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", root, err)
		}
		resumes = append(resumes, rs...)
	}
	return resumes, ctx.Err()
}

func (h *Harness) recoverRepo(root string) ([]Resumable, error) {
	s := h.store(root)
	goals, err := s.Goals()
	if err != nil {
		return nil, err
	}
	names := []string{queue.IntakeGoal}
	for _, g := range goals {
		names = append(names, g.Name)
	}
	var resumes []Resumable
	for _, name := range names {
		rs, err := h.recoverGoal(s, name)
		if err != nil {
			return nil, fmt.Errorf("goal %s: %w", name, err)
		}
		resumes = append(resumes, rs...)
	}
	return resumes, nil
}

func (h *Harness) recoverGoal(s *queue.Store, goal string) ([]Resumable, error) {
	tasks, err := s.Tasks(goal)
	if err != nil {
		return nil, err
	}
	active := map[string]*queue.Task{}
	for _, t := range tasks {
		if t.State == queue.Active {
			active[t.ID] = t
		}
	}
	if len(active) == 0 {
		return nil, nil
	}
	dirs, err := os.ReadDir(s.SessionsDir(goal))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var resumes []Resumable
	// Newest first: the names start with the time.
	for _, d := range slices.Backward(dirs) {
		dir := filepath.Join(s.SessionsDir(goal), d.Name())
		r, ok, err := h.recoverSession(s, dir, active)
		if err != nil {
			return nil, err
		}
		if ok {
			resumes = append(resumes, r)
		}
	}
	for _, t := range active {
		h.log().Warn("recovering task left active", "repo", s.Repo(), "goal", goal, "task", t.ID)
		if err := s.Move(goal, t, queue.Pending); err != nil {
			return nil, err
		}
	}
	return resumes, nil
}

// recoverSession reports whether the session in dir can be resumed, and
// takes its tasks out of active.
func (h *Harness) recoverSession(
	s *queue.Store,
	dir string,
	active map[string]*queue.Task,
) (Resumable, bool, error) {
	spec, err := session.Load(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Resumable{}, false, nil
	}
	if err != nil {
		return Resumable{}, false, err
	}
	st, err := session.LoadState(dir)
	if err != nil || st.Settled ||
		!slices.ContainsFunc(spec.Tasks, func(id string) bool { return active[id] != nil }) {
		return Resumable{}, false, err
	}
	var res sessionResult
	agentDone, err := session.ReadResult(dir, &res)
	if err != nil {
		return Resumable{}, false, err
	}
	if !agentDone && (st.AgentSession == "" || st.Resumes >= maxResumes) {
		// Nothing to carry on: the tasks start over in a new session.
		return Resumable{}, false, session.UpdateState(
			dir,
			func(st *session.State) { st.Settled = true },
		)
	}
	for _, id := range spec.Tasks {
		delete(active, id)
	}
	r := Resumable{Repo: s.Repo(), Dir: dir, Spec: spec}
	if !agentDone {
		r.AgentSession = st.AgentSession
	}
	return r, true, nil
}

// Resume carries on a session the last scheduler stopped: the agent's own
// session again, told it was stopped, or when the agent had finished, the
// gate and the commit.
func (h *Harness) Resume(ctx context.Context, r Resumable) error {
	err := h.resume(ctx, r)
	if errors.Is(err, errSuspended) {
		h.log().Info("session suspended again", "session", r.Spec.ID)
		return nil
	}
	return err
}

func (h *Harness) resume(ctx context.Context, r Resumable) error {
	cfg, err := h.config(r.Repo)
	if err != nil {
		return err
	}
	repo := Repo{Store: h.store(r.Repo), Config: cfg}
	g, err := repo.Store.Goal(r.Spec.Goal)
	if err != nil {
		return err
	}
	b := schedule.Batch{
		Repo:       r.Repo,
		Goal:       g.Name,
		Workstream: r.Spec.Workstream,
		Kind:       r.Spec.Kind,
		Profile:    r.Spec.Profile,
		Effort:     r.Spec.Effort,
	}
	for _, id := range r.Spec.Tasks {
		t, err := repo.Store.Task(g.Name, id)
		if err != nil {
			return err
		}
		b.Tasks = append(b.Tasks, t)
	}
	wt := git.Repo{Dir: r.Spec.Worktree}
	h.log().Info("resuming a stopped session", "session", r.Spec.ID, "goal", g.Name,
		"workstream", b.Workstream, "agentDone", r.AgentSession == "")
	if r.AgentSession == "" {
		var res sessionResult
		if _, err := session.ReadResult(r.Dir, &res); err != nil {
			return err
		}
		var runErr error
		if res.Error != "" {
			runErr = errors.New(res.Error)
		}
		return h.settleSession(ctx, repo, g, b, wt, r.Dir, res.Result, runErr)
	}
	if !planningKind(b.Kind) {
		if strings.TrimSpace(cfg.Gate) == "" {
			return h.askAll(repo.Store, g.Name, b.Tasks, noGate)
		}
		// The Stop hook reads the gate from the spec, which an older diatom
		// or config may have written: it checks against the config of now.
		r.Spec.Gate, r.Spec.GateAttempts, r.Spec.GateTimeout = cfg.SessionGate(), cfg.GateAttempts,
			cfg.GateTimeout
		if err := session.Create(r.Dir, r.Spec); err != nil {
			return err
		}
	}
	if err := session.UpdateState(r.Dir, func(st *session.State) { st.Resumes++ }); err != nil {
		return err
	}
	return h.runAgent(ctx, repo, g, b, wt, r.Dir, r.Spec, r.AgentSession, resumeNote)
}
