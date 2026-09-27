package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/queue"
)

func TestLandingsQueue(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "late", State: queue.GoalDone,
		Base: "main"}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	s := a.status
	// Another goal is landing.
	s.busy, s.busyGoal = "merging it into main", "set"
	var row *goalRow
	for i := range s.rows {
		if s.rows[i].goal.Name == "late" {
			row = &s.rows[i]
		}
	}
	for range 2 {
		if s.finishKey(row, "P") != nil {
			t.Fatal("a landing started beside another")
		}
	}
	if !s.landing("late") || !strings.Contains(s.flash, "late lands after set") {
		t.Fatalf("queued = %+v, flash %q", s.queued, s.flash)
	}
	if s.finishKey(row, "P") != nil || !strings.Contains(s.flash, "landing already") {
		t.Errorf("queued twice: %q", s.flash)
	}
	// Next moves past it, the nav and its page say it waits, with nothing
	// offered.
	a.next.reload()
	for _, it := range a.next.items {
		if it.row.goal.Name == "late" && it.kind == itemFinish {
			t.Error("Next still offers the goal waiting to land")
		}
	}
	a.renderNav()
	if nav := plain(a.renderNav()); !strings.Contains(nav, "waiting to land") {
		t.Errorf("the nav:\n%s", nav)
	}
	if lines := strings.Join(s.jobLines("late", 10), "\n"); !strings.Contains(plain(lines),
		"⏳ merging it into main once set has landed") {
		t.Errorf("its page = %q", lines)
	}
	if foot := plain(strings.Join(s.foot(), "\n")); !strings.Contains(foot,
		"late: merging it into main, after set") {
		t.Errorf("foot = %q", foot)
	}
	// Once the first ends, the next starts, and what the first came to stays.
	if cmd := s.jobDone(jobMsg{err: errors.New("the gate failed")}); cmd == nil {
		t.Fatal("the queued landing didn't start")
	}
	if s.busyGoal != "late" || len(s.queued) != 0 || s.err == nil ||
		!strings.HasPrefix(s.err.Error(), "set: ") {
		t.Errorf("after = %q, %v, %v", s.busyGoal, s.queued, s.err)
	}
	openGoal(t, a, "late")
	if acts := s.detailActions(); acts != nil {
		t.Errorf("actions while landing = %v", acts)
	}
	if cmd := s.jobDone(jobMsg{flash: "merged"}); cmd != nil || s.flash != "late: merged" {
		t.Errorf("the last = %q", s.flash)
	}
}
