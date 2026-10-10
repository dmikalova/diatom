package finish

import (
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
)

// TestDrop pins that giving up on a goal closes its work and clears its
// branches, and leaves its files behind to count what it cost.
func TestDrop(t *testing.T) {
	f := newFixture(t)
	f.goal.State = queue.GoalActive
	if err := f.store.SaveGoal(f.goal); err != nil {
		t.Fatal(err)
	}
	f.git("branch", f.goal.WorkstreamBranch("engine"))
	task := &queue.Task{Title: "Add ward", Workstream: "engine"}
	if err := f.store.AddTask("set", task); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddQuestion(
		"set",
		&queue.Question{Task: task.ID, Text: "Which?"},
	); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddNote(
		"set",
		&queue.Note{Task: task.ID, Text: "See DIP-3999."},
	); err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1000, 0).UTC()
	if err := Drop(f.ctx, f.store, f.goal, "DIP-4225 duplicates DIP-3999", now); err != nil {
		t.Fatal(err)
	}

	g, err := f.store.Goal("set")
	if err != nil {
		t.Fatal(err)
	}
	if g.State != queue.GoalDropped || !g.Finished.Equal(now) ||
		g.Reason != "DIP-4225 duplicates DIP-3999" {
		t.Errorf("the goal is %+v", g)
	}
	if !g.Over() {
		t.Error("a dropped goal isn't over")
	}
	done, err := f.store.Task("set", task.ID)
	if err != nil || done.State != queue.Done {
		t.Fatalf("the task is %+v, %v", done, err)
	}
	if open, _ := f.store.Questions("set", queue.QuestionOpen); len(open) > 0 {
		t.Errorf("%d questions are still open", len(open))
	}
	if unread, _ := f.store.Notes("set", queue.NoteOpen); len(unread) > 0 {
		t.Errorf("%d notes are still unread", len(unread))
	}
	for _, b := range []string{f.goal.IntegrationBranch(), f.goal.WorkstreamBranch("engine")} {
		if f.repo.BranchExists(f.ctx, b) {
			t.Errorf("branch %s is still there", b)
		}
	}
	// Dropping it again says so rather than doing it twice.
	if err := Drop(f.ctx, f.store, g, "again", now); err == nil {
		t.Error("a dropped goal was dropped again")
	}
}
