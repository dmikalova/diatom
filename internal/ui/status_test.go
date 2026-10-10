package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestLandActionsFollowTheConfig(t *testing.T) {
	keys := func(land string) string {
		var out []string
		for _, a := range landActions(&goalRow{goal: &queue.Goal{Base: "main"}, land: land}) {
			out = append(out, a.key)
		}
		return strings.Join(out, " ")
	}
	if keys("") != "P F" || keys(config.LandMerge) != "P" || keys(config.LandPRs) != "F" {
		t.Errorf("land actions: %q, %q, %q", keys(""), keys(config.LandMerge), keys(config.LandPRs))
	}
	s := &Status{}
	row := &goalRow{goal: &queue.Goal{Name: "g", Base: "main"}, land: config.LandMerge}
	if s.landsBy(row, "F") || !strings.Contains(s.flash, "P merges") || !s.landsBy(row, "P") {
		t.Errorf("landsBy with land = merge: %q", s.flash)
	}
}

// TestGoalsWaitingOnAMergeSink pins that a goal whose pull requests are open
// drops below the goals diatom can still move on.
func TestGoalsWaitingOnAMergeSink(t *testing.T) {
	f := newFixture(t)
	for _, g := range []*queue.Goal{
		{Name: "ward", State: queue.GoalDone, Base: "main", Created: time.Unix(10, 0)},
		{Name: "poison", State: queue.GoalActive, Base: "main", Created: time.Unix(20, 0)},
	} {
		if err := f.store.CreateGoal(g); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStatus(context.Background(), f.env)
	order := func() []string {
		s.reload()
		var names []string
		for _, r := range s.rows {
			if !r.intake {
				names = append(names, r.goal.Name)
			}
		}
		return names
	}
	if got := order(); !slices.Equal(got, []string{"set", "ward", "poison"}) {
		t.Fatalf("goals before landing = %v", got)
	}
	if err := finish.Save(f.store.GoalDir("ward"), &finish.Result{
		Stack: []finish.PR{{Branch: "diatom/ward/pr/1", Tip: "abc"}},
		Landing: &finish.Landing{Remote: "origin", How: finish.PRs, PRs: []finish.PRState{{
			URL: "https://example.com/pr/7", State: "OPEN"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := order(); !slices.Equal(got, []string{"set", "poison", "ward"}) {
		t.Errorf("goals once ward's pull request is open = %v, want ward last", got)
	}
}
