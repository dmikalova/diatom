package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAProblemRepeatsRatherThanPilesUp pins that the same failure hit again
// replaces the open problem and counts, so a goal diatom keeps tripping on
// asks the human once.
func TestAProblemRepeatsRatherThanPilesUp(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), ".diatom")}
	now := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(s.GoalDir("set"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGoal(
		&Goal{Name: "set", Title: "Set", State: GoalActive, Created: now},
	); err != nil {
		t.Fatal(err)
	}
	hit := func(op, text string, at time.Time) {
		t.Helper()
		if err := s.HitProblem("set", &Problem{
			Kind: ProblemCommit, What: "A batch failed", Op: op, Text: text, Created: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	hit("batch engine", "first\n", now)
	hit("batch engine", "second\n", now.Add(time.Minute))
	hit("batch cards", "other\n", now.Add(2*time.Minute))

	open, err := s.Problems("set", ProblemOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("open problems = %d, want 2", len(open))
	}
	first := open[0]
	if first.Count != 2 {
		t.Errorf("count = %d, want 2", first.Count)
	}
	if !first.Created.Equal(now) {
		t.Errorf("created = %v, want the first time it was hit", first.Created)
	}
	if first.Text != "second\n" {
		t.Errorf("text = %q, want the latest output", first.Text)
	}
	if got := first.Summary(); got != "A batch failed (2 times)" {
		t.Errorf("summary = %q", got)
	}

	first.Reply = "Try it with the lock held."
	if err := s.SaveProblem("set", first); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseProblem("set", first); err != nil {
		t.Fatal(err)
	}
	if open, err = s.Problems("set", ProblemOpen); err != nil || len(open) != 1 {
		t.Fatalf("open after closing = %d, %v", len(open), err)
	}
	done, err := s.Problems("set", ProblemDone)
	if err != nil || len(done) != 1 || done[0].Reply == "" {
		t.Fatalf("done = %v, %v", done, err)
	}
}

// TestAProblemsFixesFollowItsKind pins that a closed pull request offers the
// two ways out only it has, and a plain failure offers none.
func TestAProblemsFixesFollowItsKind(t *testing.T) {
	if got := (&Problem{Kind: ProblemCommit}).Fixes(); got != nil {
		t.Errorf("a commit failure offers %v, want nothing beyond the three", got)
	}
	fixes := (&Problem{Kind: ProblemPRClosed}).Fixes()
	if len(fixes) != 2 || !fixes[0].Value || fixes[1].Set != ReopenFix {
		t.Errorf("a closed pull request offers %+v", fixes)
	}
}
