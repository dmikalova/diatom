package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// origin gives the fixture's repo a bare remote to push to.
func (f *fixture) origin() {
	f.t.Helper()
	bare := filepath.Join(f.t.TempDir(), "origin.git")
	if _, err := f.main.Run(context.Background(), "init", "--bare", "--quiet", bare); err != nil {
		f.t.Fatal(err)
	}
	f.git("remote", "add", "origin", bare)
	f.git("push", "--quiet", "origin", "main")
}

func (f *fixture) git(args ...string) {
	f.t.Helper()
	if _, err := f.main.Run(context.Background(), args...); err != nil {
		f.t.Fatal(err)
	}
}

// ghStub answers the three gh calls the CI watch makes, with the conclusion
// its caller sets.
type ghStub struct {
	mu         sync.Mutex
	status     string
	conclusion string
	calls      []string
}

func (s *ghStub) run(_ context.Context, _ string, args ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, strings.Join(args, " "))
	line := strings.Join(args, " ")
	switch {
	case strings.Contains(line, "headRefOid"):
		return "0123456789abcdef0123456789abcdef01234567", nil
	case strings.Contains(line, "statusCheckRollup"):
		body, _ := json.Marshal(map[string]any{
			"state": "OPEN",
			"statusCheckRollup": []map[string]string{
				{"name": "matrix", "status": s.status, "conclusion": s.conclusion},
			},
		})
		return string(body), nil
	case strings.HasPrefix(line, "pr view"):
		return "https://example.com/pull/7", nil
	}
	return "", nil
}

func TestTaskWaitsForTheChecksItAskedFor(t *testing.T) {
	f := newFixture(t)
	f.origin()
	task := f.add("engine", "Make CI run the matrix")
	gh := &ghStub{status: "IN_PROGRESS"}
	f.h.GH = gh.run
	now := time.Now()
	f.h.Now = func() time.Time { return now }

	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "matrix.yml", "jobs\n")
		if err := session.Append(s.dir, s.spec, session.Entry{
			Type: session.EntryCI, Task: task.ID,
			Text: "the matrix only runs on a pull request",
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.step()
	if got := f.task(task.ID); got.CI == "" {
		t.Fatalf("task is not waiting for any commit's checks: %+v", got)
	}

	// While the checks run, the task is not scheduled.
	now = now.Add(5 * time.Minute)
	f.agent.act = func(t *testing.T, _ string, _ agentSession) {
		t.Error("the task ran while its checks were still going")
	}
	if batches := f.step(); len(batches) != 0 {
		t.Fatalf("batches while the checks run = %+v", batches)
	}

	gh.status, gh.conclusion = "COMPLETED", "SUCCESS"
	now = now.Add(5 * time.Minute)
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.report(session.EntryDone, task.ID, "")
	}
	f.step()
	if got := f.task(task.ID); got.State != queue.Done || got.CIRounds != 1 ||
		!strings.Contains(got.Body, "passed") ||
		!strings.Contains(got.Body, "https://example.com/pull/7") {
		t.Errorf("task after the checks passed = %+v", got)
	}
}

func TestAFailedPushAsksTheHuman(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Make CI run the matrix")
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "matrix.yml", "jobs\n")
		if err := session.Append(s.dir, s.spec, session.Entry{
			Type: session.EntryCI, Task: task.ID, Text: "it only runs on a pull request",
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.step()
	qs, err := f.store.Questions("set", queue.QuestionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.task(task.ID); got.CI != "" {
		t.Errorf("task waits for checks that will never run: %+v", got)
	}
	if len(qs) != 1 || qs[0].Task != task.ID ||
		!strings.Contains(qs[0].Text, "The goal's pull request could not be opened") {
		t.Errorf("questions = %+v", qs)
	}
}

// TestAskingForCIWithNothingCommittedTellsTheAgent covers the goal that has
// nothing to run checks on: that is the agent's to fix, so the task keeps
// its place rather than becoming a question.
func TestAskingForCIWithNothingCommittedTellsTheAgent(t *testing.T) {
	f := newFixture(t)
	f.origin()
	task := f.add("engine", "Make CI run the matrix")
	f.agent.act = func(t *testing.T, _ string, s agentSession) {
		if err := session.Append(s.dir, s.spec, session.Entry{
			Type: session.EntryCI, Task: task.ID, Text: "it only runs on a pull request",
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.step()
	qs, err := f.store.Questions("set", queue.QuestionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 0 {
		t.Errorf("nothing committed asked the human: %+v", qs)
	}
	got := f.task(task.ID)
	if got.CI != "" || !strings.Contains(got.Body, "no commits of its own yet") {
		t.Errorf("task after asking with nothing committed = %+v", got)
	}
}
