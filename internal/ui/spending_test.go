package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/session"
)

func TestSpendingByDay(t *testing.T) {
	f := newFixture(t)
	f.env.Now = time.Now
	write(t, filepath.Join(f.repo, ".diatom", "config.toml"), "[budget]\nday = 3\nweek = 100\n")
	// Today: a session of the set's, on its first task, and triage's.
	dir := filepath.Join(f.store.SessionsDir("set"), "20260927T100000Z-engine")
	if err := session.Create(dir, session.Spec{Workstream: "engine", Kind: queue.Planned,
		Tasks: []string{"0001", "0009"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteResult(
		dir,
		map[string]any{"usage": map[string]any{"costUSD": 4}},
	); err != nil {
		t.Fatal(err)
	}
	if err := recordSession(f.store.SessionsDir(queue.IntakeGoal), time.Now(), "m",
		runner.Result{Usage: runner.Usage{CostUSD: 0.5}}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	a.openEntry(entrySpending)
	a.setFocus(partMain)
	out := plain(a.render())
	for _, want := range []string{"💰 Spending", "Budget: $3.00 a day · $100 a week",
		"today $4.50", "· today     $4.50 █"} {
		if !strings.Contains(out, want) {
			t.Errorf("the spending lacks %q:\n%s", want, out)
		}
	}
	if goal, about := a.onScreen(); goal != "" || !strings.Contains(about, "day by day") {
		t.Errorf("on screen = %q, %q", goal, about)
	}
	key(a, "j", "k", "g", "enter")
	out = plain(a.render())
	setAt, triageAt := strings.Index(out, "set  $4.00"), strings.Index(out, "Triage  $0.50")
	if !a.spendOpen || setAt < 0 || triageAt < setAt || !strings.Contains(out, "2 sessions") ||
		!strings.Contains(out, "engine · planned · $4.00 · 0001 Add ward, 0009") {
		t.Errorf("the day opened:\n%s", out)
	}
	key(a, "j", "space", "shift+space", "k", "esc")
	if a.spendOpen || a.focus != partMain {
		t.Error("esc didn't close the day")
	}
	key(a, "esc")
	if a.focus != partNav {
		t.Error("esc didn't go back to the nav")
	}
	// The footer's spending opens it too.
	a.openLog()
	a.renderNav()
	for y, row := range a.rowEntry {
		if row == rowSpend {
			a.click(tea.Mouse{X: 1, Y: y, Button: tea.MouseLeft})
			break
		}
	}
	if a.selected().kind != entrySpending {
		t.Errorf("clicking the spending selected %+v", a.selected())
	}
}

func TestSpendingWithNothingSpent(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	a.openEntry(entrySpending)
	a.setFocus(partMain)
	key(a, "enter")
	out := plain(a.render())
	if a.spendOpen || !strings.Contains(out, "Nothing spent in the last 30 days.") ||
		!strings.Contains(out, "No budget") {
		t.Errorf("nothing spent:\n%s", out)
	}
}
