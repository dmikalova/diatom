package harness

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/workspace"
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

// TestADoneGoalsFixIsApplied pins that the pull request the human pastes
// replaces the closed one. A done goal is never scheduled, so its problem is
// taken in before the goals are run, not while they are (ADR 0014).
func TestADoneGoalsFixIsApplied(t *testing.T) {
	f := newFixture(t)
	g, _ := f.store.Goal("set")
	g.State = queue.GoalDone
	if err := f.store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	const old, moved = "https://github.com/org/repo/pull/1",
		"https://github.com/org/repo/pull/2"
	if err := finish.Save(f.store.GoalDir("set"), &finish.Result{
		Stack: []finish.PR{{Branch: g.IntegrationBranch(), Commits: []string{"feat: it"}}},
		Landing: &finish.Landing{
			Remote: "origin",
			How:    finish.PRs,
			PRs:    []finish.PRState{{URL: old, State: "CLOSED"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.HitProblem("set", &queue.Problem{
		Kind:    queue.ProblemPRClosed,
		What:    "A pull request of set was closed without being merged",
		Op:      "land",
		Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	open, err := f.store.Problems("set", queue.ProblemOpen)
	if err != nil || len(open) != 1 {
		t.Fatalf("open problems = %d, %v", len(open), err)
	}
	open[0].Fix = moved
	if err := f.store.SaveProblem("set", open[0]); err != nil {
		t.Fatal(err)
	}

	f.step()

	if open, err = f.store.Problems("set", queue.ProblemOpen); err != nil || len(open) != 0 {
		t.Fatalf("the fix left %d problems open, %v", len(open), err)
	}
	res, err := finish.Load(f.store.GoalDir("set"))
	if err != nil || res == nil || res.Landing == nil || len(res.Landing.PRs) != 1 {
		t.Fatalf("the landing = %+v, %v", res, err)
	}
	if pr := res.Landing.PRs[0]; pr.URL != moved || pr.State != "OPEN" {
		t.Errorf("the goal is on %q, %q, want the pasted one open", pr.URL, pr.State)
	}
}

// only is the one open problem of the goal, or a failure.
func only(t *testing.T, f *fixture, goal string) *queue.Problem {
	t.Helper()
	open, err := f.store.Problems(goal, queue.ProblemOpen)
	if err != nil || len(open) != 1 {
		t.Fatalf("open problems of %s = %d, %v", goal, len(open), err)
	}
	return open[0]
}

// TestTheReposLeftOutBecomeProblems pins that a repo the window could not
// take in is said so, rather than left out in silence (ADR 0014).
func TestTheReposLeftOutBecomeProblems(t *testing.T) {
	f := newFixture(t)
	f.h.Skipped = map[string]workspace.Skip{
		"/src/api": {Kind: queue.ProblemRepo, Why: "it has no origin to name its state by"},
	}
	f.h.leftOut(f.store)

	p := only(t, f, queue.IntakeGoal)
	if p.Kind != queue.ProblemRepo || p.About != "/src/api" {
		t.Errorf(
			"problem = %s about %q, want %s about /src/api",
			p.Kind,
			p.About,
			queue.ProblemRepo,
		)
	}
	if !strings.Contains(p.Text, "no origin") {
		t.Errorf("the problem doesn't say why: %s", p.Text)
	}
}

// TestReadingPullRequestsFailingBecomesAProblem pins that one failed call to
// the GitHub CLI passes and a run of them is a problem, and that the problem
// closes itself once the pull requests read again (ADR 0014).
func TestReadingPullRequestsFailingBecomesAProblem(t *testing.T) {
	f := newFixture(t)
	g, _ := f.store.Goal("set")
	boom := errors.New("gh: not authenticated")
	f.h.watchFailed(f.store, g, boom)
	if open, err := f.store.Problems("set", queue.ProblemOpen); err != nil || len(open) != 0 {
		t.Fatalf("one failure filed %d problems, %v", len(open), err)
	}
	for range prTries - 1 {
		f.h.watchFailed(f.store, g, boom)
	}
	p := only(t, f, "set")
	if p.Kind != queue.ProblemPR || !strings.Contains(p.Text, "not authenticated") {
		t.Errorf("problem = %s: %s", p.Kind, p.Text)
	}

	f.h.watchWorked(f.store, g)
	if open, err := f.store.Problems("set", queue.ProblemOpen); err != nil || len(open) != 0 {
		t.Fatalf("reading them again left %d problems open, %v", len(open), err)
	}
}

// TestAnUnknownConfigKeyBecomesAProblem pins that a config diatom cannot
// read is a problem the human can resolve by deleting the key, rather than
// only a line in the status pane.
func TestAnUnknownConfigKeyBecomesAProblem(t *testing.T) {
	f := newFixture(t)
	xdg := t.TempDir()
	f.h.Paths = config.Paths{XDG: xdg, Home: t.TempDir(), Key: "github.com/me/toy"}
	if err := os.WriteFile(
		filepath.Join(xdg, config.FileName),
		[]byte("wat = 1\n\n[repos.\"github.com/me\"]\nwat = 2\ngate = \"go test ./...\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if f.h.configProblem(f.store, errors.New("plain trouble")) {
		t.Error("an error that isn't an unknown key was filed as one")
	}
	if !f.h.configProblem(f.store, &config.UnknownKeyError{Key: "wat"}) {
		t.Fatal("an unknown key wasn't filed")
	}
	p := only(t, f, queue.IntakeGoal)
	if p.Kind != queue.ProblemConfigKey || p.About != "wat" {
		t.Fatalf("problem = %s about %q", p.Kind, p.About)
	}

	p.Fix = queue.DeleteFix
	if err := f.store.SaveProblem(queue.IntakeGoal, p); err != nil {
		t.Fatal(err)
	}
	done, err := f.h.applyFix(t.Context(), f.store, &queue.Goal{Name: queue.IntakeGoal}, p)
	if err != nil || !done {
		t.Fatalf("applyFix = %v, %v", done, err)
	}
	left, err := os.ReadFile(filepath.Join(xdg, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(left, []byte("wat")) {
		t.Errorf("the key is still in the config:\n%s", left)
	}
	if !bytes.Contains(left, []byte("go test ./...")) {
		t.Errorf("deleting the key took the rest of the block with it:\n%s", left)
	}
}
