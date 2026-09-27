package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// sessionDirs lists the goal's session directories.
func (f *fixture) sessionDirs() []string {
	f.t.Helper()
	dirs, err := filepath.Glob(filepath.Join(f.store.SessionsDir("set"), "*"))
	if err != nil {
		f.t.Fatal(err)
	}
	return dirs
}

// runOne plans and runs the ready batch with ctx.
func (f *fixture) runOne(ctx context.Context) {
	f.t.Helper()
	batches, repos, err := f.h.plan(context.Background(), nil)
	if err != nil || len(batches) != 1 {
		f.t.Fatalf("plan = %+v, %v", batches, err)
	}
	if err := f.h.RunBatch(ctx, repos[batches[0].Repo], batches[0]); err != nil {
		f.t.Fatal(err)
	}
}

func TestStopMidSessionResumesIt(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	ctx, stop := context.WithCancel(context.Background())
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "half\n")
		stop()
		<-s.ctx.Done()
	}
	f.runOne(ctx)

	if got := f.task(task.ID); got.State != queue.Active {
		t.Fatalf("task after a stop = %s", got.State)
	}
	dirs := f.sessionDirs()
	st, _ := session.LoadState(dirs[0])
	if len(dirs) != 1 || st.AgentSession != "agent-1" || st.Settled {
		t.Fatalf("sessions %v, state %+v", dirs, st)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "result.json")); err == nil {
		t.Error("a stopped agent's session has a result")
	}

	// A session made under an older config gets the gate of now.
	spec, _ := session.Load(dirs[0])
	spec.Gate, spec.GateTimeout = "", time.Second
	if err := session.Create(dirs[0], spec); err != nil {
		t.Fatal(err)
	}

	// The next scheduler resumes the same agent session in the same
	// worktree, with the half-done work still there.
	runStop, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var resumed string
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		mu.Lock()
		defer mu.Unlock()
		resumed = s.run.Resume
		if s.run.Prompt != resumeNote {
			t.Errorf("resumed with %q", s.run.Prompt)
		}
		if b, _ := os.ReadFile(filepath.Join(wt, "ward.txt")); string(b) != "half\n" {
			t.Errorf("the worktree lost the half-done work: %q", b)
		}
		writeFile(t, wt, "ward.txt", "ward\n")
		s.report(session.EntryDone, task.ID, "")
		cancel()
	}
	done := make(chan error)
	go func() { done <- f.h.Run(runStop, context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if resumed != "agent-1" {
		t.Errorf("resumed %q, want the stopped agent session", resumed)
	}
	if got := f.task(task.ID); got.State != queue.Done ||
		f.show("diatom/set/integration", "ward.txt") != "ward" {
		t.Errorf("after resuming, task is %s", got.State)
	}
	if st, _ := session.LoadState(dirs[0]); !st.Settled || st.Resumes != 1 {
		t.Errorf("state after resuming = %+v", st)
	}
	if len(f.sessionDirs()) != 1 {
		t.Error("resuming made a new session")
	}
	if spec, _ := session.Load(dirs[0]); spec.Gate != "check" || spec.GateTimeout != 2*time.Minute {
		t.Errorf("resumed spec = %+v, want the gate of now", spec)
	}
	if rs, err := f.h.Recover(context.Background()); err != nil || len(rs) != 0 {
		t.Errorf("Recover after it settled = %+v, %v", rs, err)
	}
}

func TestStopDuringGateRedoesGateOnly(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	ctx, stop := context.WithCancel(context.Background())
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward\n")
		s.report(session.EntryNote, task.ID, "Ward is a keyword now.")
		s.report(session.EntryDone, task.ID, "")
	}
	gated := 0
	f.h.Gate = func(ctx context.Context, _, _ string) (gate.Result, error) {
		gated++
		if gated == 1 {
			stop()
			<-ctx.Done()
			return gate.Result{}, ctx.Err()
		}
		return gate.Result{Passed: true}, nil
	}
	f.runOne(ctx)
	if got := f.task(task.ID); got.State != queue.Active || strings.Contains(got.Body, "keyword") {
		t.Fatalf("task after a stop during the gate = %s:\n%s", got.State, got.Body)
	}

	resumes, err := f.h.Recover(context.Background())
	if err != nil || len(resumes) != 1 || resumes[0].AgentSession != "" {
		t.Fatalf("Recover = %+v, %v", resumes, err)
	}
	f.agent.act = func(t *testing.T, _ string, _ agentSession) {
		t.Error("the agent ran again though it had finished")
	}
	if err := f.h.Resume(context.Background(), resumes[0]); err != nil {
		t.Fatal(err)
	}
	got := f.task(task.ID)
	if got.State != queue.Done || strings.Count(got.Body, "Ward is a keyword now.") != 1 ||
		gated != 2 {
		t.Errorf("after resuming, task is %s, gated %d times:\n%s", got.State, gated, got.Body)
	}
}

func TestSessionThatNeverStartedStartsOver(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	if err := f.store.Move("set", task, queue.Active); err != nil {
		t.Fatal(err)
	}
	// The scheduler stopped after making the session, before its agent said
	// which session it was.
	dir := filepath.Join(f.store.SessionsDir("set"), "20260101T000000Z-engine")
	if err := session.Create(dir, session.Spec{ID: filepath.Base(dir), Goal: "set",
		Workstream: "engine", Tasks: []string{task.ID}}); err != nil {
		t.Fatal(err)
	}
	if rs, err := f.h.Recover(context.Background()); err != nil || len(rs) != 0 {
		t.Fatalf("Recover = %+v, %v", rs, err)
	}
	if got := f.task(task.ID); got.State != queue.Pending {
		t.Errorf("task = %s, want it back to pending", got.State)
	}
	if st, _ := session.LoadState(dir); !st.Settled {
		t.Error("the session that never started was left to resume")
	}
}
