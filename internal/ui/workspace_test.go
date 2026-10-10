package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// second makes another repo beside the fixture's, with one active goal in
// it, and returns its store.
func second(t *testing.T, f *fixture, name, goal string) *queue.Store {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(filepath.Dir(f.repo), name)
	r := git.Repo{Dir: dir}
	write(t, filepath.Join(dir, "card.go"), "package card\n")
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
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, "feat: card"); err != nil {
		t.Fatal(err)
	}
	store := queue.Open(dir)
	if err := store.CreateGoal(&queue.Goal{Name: goal, Title: "Draw a card",
		State: queue.GoalActive, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTask(goal, &queue.Task{Title: "Draw it", Kind: queue.Planned,
		Workstream: "deck"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddQuestion(goal, &queue.Question{Task: "0001",
		Text: "How many cards does a draw take?"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestWindowOverTwoRepos pins that a window over a workspace lists both
// repos' goals, names each with its repo, and reads a goal from the repo it
// is in rather than the first.
func TestWindowOverTwoRepos(t *testing.T) {
	f := newFixture(t)
	other := second(t, f, "deck", "draw")
	f.env.Repos = []*queue.Store{f.store, other}
	f.env.Root = filepath.Dir(f.repo)

	a, _ := newApp(t, f)
	out := plain(a.render())
	// Each repo is a heading, so a goal's own line is all title.
	for _, want := range []string{"📁 vex ─", "📁 deck ─", "Draw a card"} {
		if !strings.Contains(out, want) {
			t.Errorf("the nav lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "deck · Draw a card") {
		t.Errorf("the nav still prefixes each goal with its repo:\n%s", out)
	}

	var row *goalRow
	for i := range a.status.rows {
		if a.status.rows[i].goal.Name == "draw" {
			row = &a.status.rows[i]
		}
	}
	if row == nil {
		t.Fatalf("rows = %d, none for the second repo", len(a.status.rows))
	}
	if row.store != other || row.repo != other.Repo() || row.questions != 1 {
		t.Errorf("the second repo's row = %+v", row)
	}

	// Opening it reads its tasks from its own repo, not the first.
	a.status.openDetail(row)
	if len(a.status.detail.tasks) != 1 || a.status.detail.tasks[0].Title != "Draw it" {
		t.Errorf("the page's tasks = %+v", a.status.detail.tasks)
	}

	// Next offers both repos' questions, each against its own goal.
	goals := map[string]bool{}
	for _, it := range a.next.items {
		if it.kind == itemQuestion {
			goals[it.row.goal.Name] = true
		}
	}
	if !goals["set"] || !goals["draw"] {
		t.Errorf("Next's questions come from %v", goals)
	}

	// The goal's page and Next both say which repo the goal is in.
	if page := plain(a.status.renderDetail()); !strings.Contains(page, "📁 deck ›") {
		t.Errorf("the goal's page doesn't name its repo:\n%s", page)
	}
	a.openEntry(entryNext)
	a.next.only = "draw"
	if n := plain(a.render()); !strings.Contains(n, "📁 deck") {
		t.Errorf("Next doesn't name the repo:\n%s", n)
	}
}

// TestSpendingCoversEveryRepo pins that the spending adds up both repos'
// sessions, and names each day's goals with the repo they are in.
func TestSpendingCoversEveryRepo(t *testing.T) {
	f := newFixture(t)
	f.env.Now = time.Now
	other := second(t, f, "deck", "draw")
	f.env.Repos = []*queue.Store{f.store, other}
	f.env.Root = filepath.Dir(f.repo)
	if err := f.store.SaveGoal(&queue.Goal{Name: "set", Title: "Implement the next set",
		State: queue.GoalActive, Base: "main"}); err != nil {
		t.Fatal(err)
	}

	for _, s := range []struct {
		store *queue.Store
		goal  string
		usd   float64
	}{{f.store, "set", 4}, {other, "draw", 0.5}} {
		dir := filepath.Join(s.store.SessionsDir(s.goal), "20260927T100000Z-engine")
		if err := session.Create(dir, session.Spec{Workstream: "engine", Kind: queue.Planned,
			Tasks: []string{"0001"}}); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteResult(dir,
			map[string]any{"usage": map[string]any{"costUSD": s.usd}}); err != nil {
			t.Fatal(err)
		}
	}

	a, _ := newApp(t, f)
	a.openEntry(entrySpending)
	a.setFocus(partMain)
	if !strings.Contains(plain(a.render()), "today $4.50") {
		t.Errorf("the spending doesn't add both repos up:\n%s", plain(a.render()))
	}
	key(a, "g", "enter")
	out := plain(a.render())
	for _, want := range []string{"vex · Implement the next set", "deck · Draw a card"} {
		if !strings.Contains(out, want) {
			t.Errorf("the day lacks %q:\n%s", want, out)
		}
	}
}
