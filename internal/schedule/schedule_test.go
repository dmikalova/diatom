package schedule

import (
	"slices"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
)

func task(id string, kind queue.Kind, ws string) *queue.Task {
	return &queue.Task{
		ID:         id,
		Kind:       kind,
		Workstream: ws,
		Profile:    kind.DefaultProfile(),
		State:      queue.Pending,
	}
}

func ids(b Batch) []string {
	var out []string
	for _, t := range b.Tasks {
		out = append(out, t.ID)
	}
	return out
}

func TestReady(t *testing.T) {
	a := &queue.Task{ID: "1", State: queue.Done}
	b := &queue.Task{ID: "2", State: queue.Pending, DependsOn: []string{"1"}}
	c := &queue.Task{ID: "3", State: queue.Pending, DependsOn: []string{"2"}}
	d := &queue.Task{ID: "4", State: queue.Blocked}
	e := &queue.Task{ID: "5", State: queue.Pending}
	got := Ready([]*queue.Task{a, b, c, d, e})
	if len(got) != 2 || got[0] != b || got[1] != e {
		t.Errorf("Ready = %v, want tasks 2 and 5", got)
	}
}

func TestNextPriorityOrder(t *testing.T) {
	g := &Goal{Repo: "vex", Name: "set", Ready: []*queue.Task{
		task("1", queue.Planned, "engine"),
		task("2", queue.Revision, "engine"),
		task("3", queue.GateRepair, "cards"),
		task("4", queue.Conflict, "engine"),
	}}
	got := Next([]*Goal{g}, nil, Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 2}}})
	if len(got) != 2 {
		t.Fatalf("Next = %+v, want two batches", got)
	}
	if got[0].Workstream != "cards" || got[0].Kind != queue.GateRepair {
		t.Errorf("first batch = %+v, want the gate repair", got[0])
	}
	if got[1].Workstream != "engine" || got[1].Kind != queue.Conflict {
		t.Errorf("second batch = %+v, want engine's conflict, ahead of its revision", got[1])
	}
}

func TestNextBatchesSameKindAndProfile(t *testing.T) {
	strong := task("3", queue.Revision, "engine")
	strong.Profile = "planning"
	g := &Goal{Repo: "vex", Name: "set", Ready: []*queue.Task{
		task("1", queue.Revision, "engine"),
		task("2", queue.Revision, "engine"),
		strong,
		task("4", queue.Revision, "cards"),
		task("5", queue.Planned, "engine"),
		task("6", queue.Revision, "engine"),
	}}
	got := Next(
		[]*Goal{g},
		nil,
		Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 5, Batch: 2}}},
	)
	if len(got) != 2 {
		t.Fatalf("Next = %+v", got)
	}
	if !slices.Equal(ids(got[0]), []string{"1", "2"}) || got[0].Profile != "implementation" {
		t.Errorf(
			"engine batch = %v on %s, want 1 and 2 capped at MaxBatch",
			ids(got[0]),
			got[0].Profile,
		)
	}
	if !slices.Equal(ids(got[1]), []string{"4"}) {
		t.Errorf("cards batch = %v", ids(got[1]))
	}
}

func TestNextRespectsCaps(t *testing.T) {
	now := time.Now()
	vex := &Goal{Repo: "vex", Name: "set", Created: now, Ready: []*queue.Task{
		task("1", queue.Planned, "engine"), task("2", queue.Planned, "cards"),
	}}
	dots := &Goal{Repo: "dotfiles", Name: "zsh", Created: now.Add(time.Hour), Ready: []*queue.Task{
		task("1", queue.Planned, "shell"),
	}}

	// vex defaults to one session and engine is already busy, so only
	// dotfiles can start, until the machine cap is reached.
	running := []Running{{Repo: "vex", Goal: "set", Workstream: "engine"}}
	got := Next([]*Goal{vex, dots}, running, Limits{})
	if len(got) != 1 || got[0].Repo != "dotfiles" {
		t.Errorf("Next = %+v, want only the dotfiles batch", got)
	}
	if got := Next([]*Goal{vex, dots}, running, Limits{Machine: 1}); len(got) != 0 {
		t.Errorf("Next at the machine cap = %+v", got)
	}
}

func TestNextPinnedFirst(t *testing.T) {
	now := time.Now()
	old := &Goal{
		Repo:    "vex",
		Name:    "old",
		Created: now,
		Ready:   []*queue.Task{task("1", queue.GateRepair, "a")},
	}
	pinned := &Goal{Repo: "vex", Name: "pinned", Pinned: true, Created: now.Add(time.Hour),
		Ready: []*queue.Task{task("1", queue.Planned, "a")}}
	got := Next([]*Goal{old, pinned}, nil, Limits{})
	if len(got) != 1 || got[0].Goal != "pinned" {
		t.Errorf("Next = %+v, want the pinned goal first", got)
	}
}

func TestNextOrdersWithinKind(t *testing.T) {
	late := task("1", queue.Planned, "a")
	late.Priority = 2
	early := task("2", queue.Planned, "a")
	early.Priority = 1
	g := &Goal{Repo: "r", Name: "g", Ready: []*queue.Task{late, early}}
	got := Next([]*Goal{g}, nil, Limits{Repos: map[string]RepoLimits{"r": {Sessions: 1, Batch: 1}}})
	if len(got) != 1 || got[0].Tasks[0] != early {
		t.Errorf("Next = %+v, want the lower priority number first", got)
	}
}

func TestRankUnknownKindLast(t *testing.T) {
	if Rank("mystery") <= Rank(queue.Planned) {
		t.Error("an unknown kind ranks ahead of planned work")
	}
}

func TestNextBatchesSameEffort(t *testing.T) {
	retry := task("2", queue.Planned, "engine")
	retry.Effort = "high"
	g := &Goal{
		Repo:  "r",
		Name:  "g",
		Ready: []*queue.Task{task("1", queue.Planned, "engine"), retry},
	}
	got := Next([]*Goal{g}, nil, Limits{})
	if len(got) != 1 || !slices.Equal(ids(got[0]), []string{"1"}) || got[0].Effort != "" {
		t.Errorf("Next = %+v, want the retry left for its own batch", got)
	}
	g.Ready = []*queue.Task{retry}
	if got := Next([]*Goal{g}, nil, Limits{}); len(got) != 1 || got[0].Effort != "high" {
		t.Errorf("retry batch = %+v, want its effort", got)
	}
}
