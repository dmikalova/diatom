package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/spend"
	"github.com/dmikalova/diatom/internal/tui"
)

func TestFinishedGoalsListLatestFirst(t *testing.T) {
	f := newFixture(t)
	// What was spent counts up to now.
	f.env.Now = time.Now
	for _, g := range []*queue.Goal{
		{Name: "old", Title: "Old set", Description: "What the old set was for.",
			State: queue.GoalFinished, Finished: time.Date(2026, 9, 1, 10, 0, 0, 0, time.Local)},
		{Name: "new", Title: "New set", State: queue.GoalFinished,
			Finished: time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local)},
	} {
		if err := f.store.CreateGoal(g); err != nil {
			t.Fatal(err)
		}
	}
	// Its cost still counts once it has finished.
	if err := recordSession(f.store.SessionsDir("new"), time.Now(), "mechanical",
		runner.Result{Usage: runner.Usage{CostUSD: 12}}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	es := a.entries()
	if es[len(es)-3].kind != entryFinished || es[len(es)-2].kind != entryLog ||
		es[len(es)-1].kind != entrySpending {
		t.Fatalf("the menu = %+v", es[len(es)-3:])
	}
	nav := plain(a.renderNav())
	if at := strings.Index(nav, "☑ Finished (2)"); at < 0 || at > strings.Index(nav, "📒") ||
		strings.Index(nav, "📒") > strings.Index(nav, "💰") {
		t.Errorf("the menu isn't Finished, the log, then Spending:\n%s", nav)
	}
	if strings.Contains(nav, "Old set") {
		t.Errorf("a finished goal is in the nav:\n%s", nav)
	}
	if !strings.Contains(plain(a.footer()), "D$12") {
		t.Errorf("the finished goal's cost is gone: %q", plain(a.footer()))
	}
	a.sel = len(es) - 3
	a.show()
	key(a, "enter")
	out := plain(a.render())
	newAt, oldAt := strings.Index(out, "New set"), strings.Index(out, "Old set")
	if newAt < 0 || oldAt < newAt || !strings.Contains(out, "new · finished Sep 20 10:00 · $12") ||
		!strings.Contains(out, "What the old set was for.") {
		t.Errorf("the finished goals:\n%s", out)
	}
	if goal, about := a.onScreen(); goal != "" || about != "the list of finished goals" {
		t.Errorf("on screen = %q, %q", goal, about)
	}
	key(a, "j", "pgdown", "pgup", "k", "g", "space")
	if a.focus != partMain {
		t.Error("scrolling left the list")
	}
	key(a, "esc")
	if a.focus != partNav {
		t.Error("esc didn't go back to the nav")
	}
}

func TestFinishedWithoutATime(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "old", State: queue.GoalFinished}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	if len(a.finished) != 1 || a.finished[0].at.IsZero() {
		t.Errorf("finished = %+v", a.finished)
	}
	out := plain(a.renderFinished(80, 20))
	if !strings.Contains(out, "old\n") || !strings.Contains(out, "old · finished") {
		t.Errorf("a goal without a title:\n%s", out)
	}
}

func TestLandingReviewRanksWithFinishing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := git.Repo{Dir: f.repo}
	write(t, filepath.Join(f.repo, "settled.txt"), "both\n")
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "fix: settle")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"late", "later"} {
		if err := f.store.CreateGoal(&queue.Goal{Name: name, State: queue.GoalActive,
			Base: "main", Created: time.Unix(2000, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	// A settlement of a landing's rebase, and a catch-up with the base.
	if err := f.store.AddDone("late", &queue.Task{Title: "Settle", Kind: queue.Conflict,
		Origin: queue.Origin{Type: "landing"}, Commits: []string{sha}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddTask("later", &queue.Task{Title: "Catch up", Kind: queue.Conflict,
		Workstream: "engine", Merge: "abc", Commits: []string{sha}}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	var got []string
	for _, it := range a.next.items {
		got = append(got, it.id())
	}
	want := []string{"late 3", "later 3", "set question 0001", "set 3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("items = %v, want %v", got, want)
	}
}

func TestSpendingShowsTheBudget(t *testing.T) {
	f := newFixture(t)
	// What was spent counts up to now.
	f.env.Now = time.Now
	write(t, filepath.Join(f.repo, ".diatom", "config.toml"), "[budget]\nday = 1\nmonth = 100\n")
	if err := recordSession(f.store.SessionsDir("set"), time.Now(), "mechanical",
		runner.Result{Usage: runner.Usage{CostUSD: 2}}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	foot := a.footer()
	if !strings.Contains(foot, tui.Color("D$2.00", tui.Red)) ||
		strings.Contains(foot, tui.Color("M$2.00", tui.Red)) ||
		!strings.Contains(plain(foot), "today's budget is spent: nothing new starts") {
		t.Errorf("footer = %q", foot)
	}
	if money(220.4) != "$220" || money(3.456) != "$3.46" {
		t.Errorf("money = %s, %s", money(220.4), money(3.456))
	}
}

func TestRecordSessionTwiceInASecond(t *testing.T) {
	f := newFixture(t)
	at := time.Now()
	for range 2 {
		if err := recordSession(f.store.SessionsDir("set"), at, "mechanical",
			runner.Result{Usage: runner.Usage{CostUSD: 0.25}}); err != nil {
			t.Fatal(err)
		}
	}
	got := spend.New().Goal(f.store, "set")
	if len(got) != 2 || got[0].USD+got[1].USD != 0.5 {
		t.Errorf("recorded = %+v", got)
	}
	if err := recordSession(filepath.Join(f.repo, ".git", "HEAD", "x"), at, "m",
		runner.Result{}); err == nil {
		t.Error("recording under a file worked")
	}
}
