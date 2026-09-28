package ledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
	"github.com/dmikalova/diatom/internal/spend"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// landedRepo is a repo with one finished goal, whose layout's base and tip
// differ by code, test and doc lines, and one session that cost $2.50.
func landedRepo(t *testing.T) (*queue.Store, *queue.Goal) {
	t.Helper()
	ctx, dir := context.Background(), t.TempDir()
	r := git.Repo{Dir: dir}
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main"},
		{"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"}} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, ".git/info/exclude", ".diatom/\n")
	write(t, dir, "ward.go", "package ward\n")
	commit := func(msg string) string {
		if _, err := r.StageAll(ctx); err != nil {
			t.Fatal(err)
		}
		sha, err := r.Commit(ctx, msg)
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	base := commit("feat: base")
	write(t, dir, "ward.go", "package ward\n\nfunc Ward() {}\nfunc Stack() {}\n")
	write(t, dir, "ward_test.go", "package ward\n\nfunc TestWard() {}\n")
	write(t, dir, "docs/ward.md", "# Ward\n")
	tip := commit("feat: ward")
	s := queue.Open(dir)
	g := &queue.Goal{Name: "ward", Title: "Add ward", State: queue.GoalFinished, Base: "main",
		Finished: time.Date(2026, 9, 24, 12, 0, 0, 0, time.Local)}
	if err := s.CreateGoal(g); err != nil {
		t.Fatal(err)
	}
	if err := finish.Save(s.GoalDir(g.Name), &finish.Result{Base: base,
		Stack: []finish.PR{{Branch: "diatom/ward/final", Tip: tip}}}); err != nil {
		t.Fatal(err)
	}
	sd := filepath.Join(s.SessionsDir(g.Name), "20260924T100000Z-engine")
	if err := session.Create(sd, session.Spec{ID: "a", Tasks: []string{"0001"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteResult(
		sd,
		map[string]any{"usage": map[string]any{"costUSD": 2.5}},
	); err != nil {
		t.Fatal(err)
	}
	return s, g
}

func TestMeasureRecordAndBackfill(t *testing.T) {
	s, g := landedRepo(t)
	l, err := Measure(context.Background(), s, spend.New(), g)
	if err != nil {
		t.Fatal(err)
	}
	if l.Files != 3 || l.Code != (Lines{Added: 3}) || l.Tests != (Lines{Added: 3}) ||
		l.Docs != (Lines{Added: 1}) || l.LOC() != 6 || l.Sessions != 1 ||
		fmt.Sprintf("%.2f", l.CostUSD) != "2.50" {
		t.Errorf("measured %+v", l)
	}
	path := filepath.Join(t.TempDir(), "state", FileName)
	for range 2 {
		if n, err := Backfill(context.Background(), path, s, spend.New()); err != nil {
			t.Fatal(err)
		} else if all, _ := Load(path); len(all) != 1 || n > 1 {
			t.Fatalf("after a backfill: %d added, ledger %+v", n, all)
		}
	}
	if err := Record(path, l); err != nil {
		t.Fatal(err)
	}
	if all, _ := Load(path); len(all) != 1 {
		t.Errorf("recording a goal twice keeps %d", len(all))
	}
}

func TestWeeks(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.Local) }
	ls := []Landed{
		{Finished: day(21), Code: Lines{Added: 100}, CostUSD: 10}, // a Monday
		{Finished: day(27), Tests: Lines{Added: 50}, CostUSD: 5},  // that Sunday
		{Finished: day(28), Code: Lines{Added: 40}, CostUSD: 1},   // the next Monday
		{Finished: day(28), Docs: Lines{Added: 999}, CostUSD: 1},  // docs aren't code
	}
	weeks := Weeks(ls)
	if len(weeks) != 2 || !weeks[0].Start.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.Local)) ||
		weeks[0].LOC != 40 || weeks[0].Docs != 999 || weeks[1].LOC != 150 ||
		fmt.Sprintf("%.0f", weeks[1].PerDollar()) != "10" {
		t.Errorf("weeks = %+v", weeks)
	}
	if p := Sum(nil); fmt.Sprintf("%.2f", p.PerDollar()) != "0.00" {
		t.Errorf("nothing spent = %v lines/$", p.PerDollar())
	}
}
