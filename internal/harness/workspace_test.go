package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

// addRepo makes a second repo beside the fixture's, with one active goal and
// one ready task, and adds it to the harness.
func addRepo(t *testing.T, f *fixture, goal, ws string) *queue.Store {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	r := git.Repo{Dir: dir}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", "T"},
		{"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, dir, ".git/info/exclude", ".diatom/\n")
	writeFile(t, dir, "shared.txt", "base\n")
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, "chore: start"); err != nil {
		t.Fatal(err)
	}
	s := queue.Open(dir)
	if err := s.CreateGoal(&queue.Goal{
		Name: goal, Title: goal, State: queue.GoalActive, Base: "main",
		Workstreams: []queue.Workstream{{Name: ws}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTask(goal, &queue.Task{
		Title: "Do it", Kind: queue.Planned, Workstream: ws,
		Origin: queue.Origin{Type: "plan"},
	}); err != nil {
		t.Fatal(err)
	}
	f.h.Roots = append(f.h.Roots, dir)
	return s
}

// TestPlanCoversEveryRepo pins that one scheduler over a workspace plans
// every repo's work, and names each batch with the repo it belongs to.
func TestPlanCoversEveryRepo(t *testing.T) {
	f := newFixture(t)
	f.add("engine", "Add the engine")
	other := addRepo(t, f, "deck", "shell")

	batches, repos, err := f.h.plan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("plan loaded %d repos, want both", len(repos))
	}
	got := map[string]string{}
	for _, b := range batches {
		got[b.Repo] = b.Goal
	}
	if got[f.store.Repo()] != "set" || got[other.Repo()] != "deck" {
		t.Errorf("batches = %v, want one goal from each repo", got)
	}
}

// TestPlanSharesTheSessionLimit pins that the first repo's limit is the
// whole workspace's, and that each repo still keeps one session of it.
func TestPlanSharesTheSessionLimit(t *testing.T) {
	f := newFixture(t)
	// The workspace runs two sessions at once, and each repo may use two.
	f.add("engine", "Add the engine")
	f.add("cards", "Add the cards")
	other := addRepo(t, f, "deck", "shell")

	batches, _, err := f.h.plan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	per := map[string]int{}
	for _, b := range batches {
		per[b.Repo]++
	}
	if len(batches) != 2 || per[f.store.Repo()] != 1 || per[other.Repo()] != 1 {
		t.Errorf("batches = %v, want one in each repo under the shared limit of two", per)
	}
}

// TestRecoverReadsEveryRepo pins that the sessions a stopped scheduler left
// are found in every repo, not only the first.
func TestRecoverReadsEveryRepo(t *testing.T) {
	f := newFixture(t)
	other := addRepo(t, f, "deck", "shell")
	// A repo with nothing unsettled recovers nothing, and doesn't fail.
	resumes, err := f.h.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(resumes) != 0 {
		t.Fatalf("Recover = %+v, want none", resumes)
	}
	if _, err := os.Stat(filepath.Join(other.Root, "goals")); err != nil {
		t.Errorf("the second repo has no queue: %v", err)
	}
}

// triage runs a triage report against the fixture's intake goal, as a
// session would hand it in, and returns the intake goal.
func triage(t *testing.T, f *fixture, report session.Report) *queue.Goal {
	t.Helper()
	ctx := context.Background()
	if err := f.h.ensureIntakeGoal(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	g, err := f.store.Goal(queue.IntakeGoal)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(f.store.Repo(), f.h.Paths)
	if err != nil {
		t.Fatal(err)
	}
	repo := Repo{Store: f.store, Config: cfg}
	ask := func(task, text string) error {
		return f.store.AddQuestion(g.Name, &queue.Question{Task: task, Text: text})
	}
	if err := f.h.applyTriage(ctx, repo, report, queue.Origin{Type: "triage"}, ask); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestTriagePlacesAGoalInTheRepoItNames pins that triage over a workspace
// starts a goal in the repo it chose, not the one it ran in.
func TestTriagePlacesAGoalInTheRepoItNames(t *testing.T) {
	f := newFixture(t)
	other := addRepo(t, f, "deck", "shell")
	name := filepath.Base(other.Repo())

	triage(t, f, session.Report{Goals: []session.Entry{{
		Task: "0001", Type: session.EntryGoal, Repo: name,
		Title: "Shuffle the deck", Description: "Deal a random hand.",
	}}})

	goals, err := other.Goals()
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 2 {
		t.Fatalf("the named repo's goals = %d, want its own and the new one", len(goals))
	}
	mine, err := f.store.Goals()
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Errorf("the session's own repo gained a goal: %d", len(mine))
	}
}

// TestTriageFallsBackToTheIntakesRepo pins that a goal naming no repo lands
// in the repo the intake was sent to, rather than asking the human.
func TestTriageFallsBackToTheIntakesRepo(t *testing.T) {
	f := newFixture(t)
	other := addRepo(t, f, "deck", "shell")
	g := triage(t, f, session.Report{Goals: []session.Entry{{
		Task: "0001", Type: session.EntryGoal,
		Title: "Shuffle the deck", Description: "Deal a random hand.",
	}}})

	qs, err := f.store.Questions(g.Name, queue.QuestionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 0 {
		t.Errorf("a goal with no repo asked %+v", qs)
	}
	if !hasTitle(t, f.store, "Shuffle the deck") {
		t.Error("the goal didn't land in the repo the intake was sent to")
	}
	if hasTitle(t, other, "Shuffle the deck") {
		t.Error("the goal landed in the other repo")
	}
}

func hasTitle(t *testing.T, s *queue.Store, title string) bool {
	t.Helper()
	goals, err := s.Goals()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range goals {
		if g.Title == title {
			return true
		}
	}
	return false
}

// TestTriageAsksWhichRepoWhenItCantTell pins that a goal put in a repo the
// workspace doesn't hold goes back to the human rather than landing in the
// wrong repo.
func TestTriageAsksWhichRepoWhenItCantTell(t *testing.T) {
	f := newFixture(t)
	other := addRepo(t, f, "deck", "shell")
	g := triage(t, f, session.Report{Goals: []session.Entry{{
		Task: "0001", Type: session.EntryGoal, Repo: "nowhere",
		Title: "Shuffle the deck", Description: "Deal a random hand.",
	}}})

	qs, err := f.store.Questions(g.Name, queue.QuestionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || !strings.Contains(qs[0].Text, "Which of these repos") {
		t.Fatalf("an unknown repo asked %+v", qs)
	}
	if !strings.Contains(qs[0].Text, filepath.Base(other.Repo())) {
		t.Errorf("the question doesn't name the repos to choose from: %s", qs[0].Text)
	}
	for _, s := range []*queue.Store{f.store, other} {
		if hasTitle(t, s, "Shuffle the deck") {
			t.Errorf("the goal was started in %s anyway", s.Repo())
		}
	}
}

// TestTriagePromptNamesTheRepos pins that triage over a workspace is told
// which repos it may place a goal in, and sees each repo's goals.
func TestTriagePromptNamesTheRepos(t *testing.T) {
	f := newFixture(t)
	other := addRepo(t, f, "deck", "shell")
	ctx := context.Background()
	if err := f.h.ensureIntakeGoal(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	g, _ := f.store.Goal(queue.IntakeGoal)
	cfg, _ := config.Load(f.store.Repo(), f.h.Paths)
	ws, err := f.h.workspace()
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.h.planningPrompt(Repo{Store: f.store, Config: cfg}, g, PromptInput{
		Goal: g, Batch: schedule.Batch{Kind: queue.Triage}, Repos: ws,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[-repo <repo>]",
		"Pass -repo with the repo the goal belongs in",
		"the default when you leave -repo off",
		"### Repo " + filepath.Base(other.Repo()),
		"#### deck: deck (active)",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the triage prompt lacks %q:\n%s", want, p)
		}
	}
}

// TestGoalNamesAreUniqueAcrossTheWorkspace pins that two goals with the same
// title in different repos get different names, so a name identifies a goal
// wherever the window shows it.
func TestGoalNamesAreUniqueAcrossTheWorkspace(t *testing.T) {
	f := newFixture(t)
	other := addRepo(t, f, "deck", "shell")
	mine, theirs := filepath.Base(f.store.Repo()), filepath.Base(other.Repo())

	for _, repo := range []string{mine, theirs} {
		triage(t, f, session.Report{Goals: []session.Entry{{
			Task: "0001", Type: session.EntryGoal, Repo: repo,
			Title: "Shuffle the deck", Description: "Deal a random hand.",
		}}})
	}
	names := map[string]bool{}
	for _, s := range []*queue.Store{f.store, other} {
		goals, err := s.Goals()
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range goals {
			if g.Title != "Shuffle the deck" {
				continue
			}
			if names[g.Name] {
				t.Errorf("two goals are both named %s", g.Name)
			}
			names[g.Name] = true
		}
	}
	if len(names) != 2 {
		t.Errorf("goals named %v, want one in each repo", names)
	}
}
