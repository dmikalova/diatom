package harness

import (
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
)

// TestAProblemParksItsGoal pins that an open problem stops the goal's work,
// as a question stops its task, and that the human's reply comes back as a
// task of the goal's and lets it run again.
func TestAProblemParksItsGoal(t *testing.T) {
	f := newFixture(t)
	f.add("engine", "Add ward")
	if err := f.store.HitProblem("set", &queue.Problem{
		Kind:    queue.ProblemCommit,
		What:    "A batch of set failed",
		Op:      "batch engine",
		Text:    "error: the gate timed out\n",
		Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	f.agent.act = func(t *testing.T, _ string, _ agentSession) {
		t.Error("a goal with an open problem ran anyway")
	}
	f.step()

	open, err := f.store.Problems("set", queue.ProblemOpen)
	if err != nil || len(open) != 1 {
		t.Fatalf("open problems = %d, %v", len(open), err)
	}
	before, err := f.store.Tasks("set")
	if err != nil {
		t.Fatal(err)
	}
	open[0].Reply = "Give the gate longer and run it again."
	if err := f.store.SaveProblem("set", open[0]); err != nil {
		t.Fatal(err)
	}

	f.agent.act = func(*testing.T, string, agentSession) {}
	f.step()
	if open, err = f.store.Problems("set", queue.ProblemOpen); err != nil || len(open) != 0 {
		t.Fatalf("the reply left %d problems open, %v", len(open), err)
	}
	after, err := f.store.Tasks("set")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("tasks went from %d to %d, want one more", len(before), len(after))
	}
	made := after[len(after)-1]
	if made.Origin.Type != "problem" {
		t.Errorf("the task came from %q, want problem", made.Origin.Type)
	}
	if want := "Give the gate longer"; !strings.Contains(made.Body, want) {
		t.Errorf("the task's body lacks the reply: %s", made.Body)
	}
	if !strings.Contains(made.Body, "the gate timed out") {
		t.Errorf("the task's body lacks what failed: %s", made.Body)
	}
}
