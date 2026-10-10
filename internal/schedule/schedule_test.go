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

func TestNextMechanicalTakesNoSession(t *testing.T) {
	g := &Goal{Repo: "vex", Name: "set",
		Ready: []*queue.Task{task("1", queue.Planned, "engine"), task("2", queue.Planned, "web")},
		Mechanical: []*queue.Task{
			task("3", queue.Conflict, "cards"), task("4", queue.Conflict, "cards"),
			task("5", queue.Conflict, "engine"), task("6", queue.Conflict, "docs"),
		},
	}
	lim := Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 1}}}
	running := []Running{
		{Repo: "vex", Goal: "set", Workstream: "docs"},
		{Repo: "vex", Goal: "set", Workstream: "old", Mechanical: true},
	}
	got := Next([]*Goal{g}, running, lim)
	if len(got) != 2 || !got[0].Mechanical || got[0].Workstream != "cards" ||
		!slices.Equal(ids(got[0]), []string{"3", "4"}) || !got[1].Mechanical ||
		got[1].Workstream != "engine" {
		t.Fatalf("Next = %+v, want cards' and engine's merges, and no session", got)
	}
	// Mechanical work running takes no session either.
	running = running[1:]
	got = Next([]*Goal{g}, running, lim)
	if len(got) != 4 || got[3].Mechanical || got[3].Workstream != "web" {
		t.Errorf("Next = %+v, want the merges, then web's session", got)
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

// TestNextKeepsASlotForEachRepo pins that a busy repo doesn't starve a quiet
// one: with two repos capped at two sessions each and the machine at three,
// the first takes two and the third is left for the second.
func TestNextKeepsASlotForEachRepo(t *testing.T) {
	now := time.Now()
	vex := &Goal{Repo: "vex", Name: "set", Created: now, Ready: []*queue.Task{
		task("1", queue.Planned, "engine"), task("2", queue.Planned, "cards"),
		task("3", queue.Planned, "web"),
	}}
	dots := &Goal{Repo: "dotfiles", Name: "zsh", Created: now.Add(time.Hour), Ready: []*queue.Task{
		task("1", queue.Planned, "shell"),
	}}
	lim := Limits{Machine: 3, Repos: map[string]RepoLimits{
		"vex": {Sessions: 3}, "dotfiles": {Sessions: 3},
	}}
	got := Next([]*Goal{vex, dots}, nil, lim)
	per := map[string]int{}
	for _, b := range got {
		per[b.Repo]++
	}
	if len(got) != 3 || per["vex"] != 2 || per["dotfiles"] != 1 {
		t.Errorf("Next = %+v, want two vex batches and one dotfiles", got)
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

// TestNextHigherGoalFirst pins that among work of the same kind, a freed
// session goes to the goal higher in the list: the older one.
func TestNextHigherGoalFirst(t *testing.T) {
	now := time.Now()
	top := &Goal{Repo: "vex", Name: "top", Created: now,
		Ready: []*queue.Task{task("5", queue.Planned, "engine")}}
	lower := &Goal{Repo: "vex", Name: "lower", Created: now.Add(time.Millisecond),
		Ready: []*queue.Task{task("1", queue.Planned, "engine")}}
	lim := Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 1}}}
	if got := Next([]*Goal{lower, top}, nil, lim); len(got) != 1 || got[0].Goal != "top" {
		t.Errorf("Next = %+v, want the top goal", got)
	}
}

// TestNextChainsDependentTasks pins that a session goes on to the tasks
// that wait only on tasks in it, in its own workstream, up to the cap.
func TestNextChainsDependentTasks(t *testing.T) {
	dep := func(tk *queue.Task, on ...string) *queue.Task { tk.DependsOn = on; return tk }
	first := task("1", queue.Planned, "uitest")
	g := &Goal{
		Repo:  "vex",
		Name:  "g",
		Ready: []*queue.Task{first},
		Later: []*queue.Task{
			dep(task("3", queue.Planned, "uitest"), "2"),
			dep(task("2", queue.Planned, "uitest"), "1"),
			dep(
				task("4", queue.Planned, "uitest"),
				"1",
				"9",
			), // 9 is another workstream's, not done
			dep(task("5", queue.Planned, "web"), "1"),
		},
		Unfinished: map[string]bool{
			"1": true,
			"2": true,
			"3": true,
			"4": true,
			"5": true,
			"9": true,
		},
	}
	got := Next([]*Goal{g}, nil, Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 1}}})
	if len(got) != 1 || !slices.Equal(ids(got[0]), []string{"1", "2", "3"}) {
		t.Fatalf("Next = %+v, want the chain 1, 2, 3", got)
	}
	got = Next(
		[]*Goal{g},
		nil,
		Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 1, Batch: 2}}},
	)
	if len(got) != 1 || !slices.Equal(ids(got[0]), []string{"1", "2"}) {
		t.Errorf("Next with a batch of 2 = %+v", got)
	}
}

// TestNextChainsOntoACoveringProfile pins that a chain goes on to a task on
// another profile when one covers the other, running on the one that does,
// and stops at a profile neither covers.
func TestNextChainsOntoACoveringProfile(t *testing.T) {
	on := func(tk *queue.Task, profile string, deps ...string) *queue.Task {
		tk.Profile, tk.DependsOn = profile, deps
		return tk
	}
	g := &Goal{
		Repo:  "vex",
		Name:  "g",
		Ready: []*queue.Task{on(task("1", queue.Planned, "vocab"), "mechanical")},
		Later: []*queue.Task{
			on(task("2", queue.Planned, "vocab"), "implementation", "1"),
			on(task("3", queue.Planned, "vocab"), "mechanical", "2"),
			on(task("4", queue.Planned, "vocab"), "research", "3"),
		},
		Unfinished: map[string]bool{"1": true, "2": true, "3": true, "4": true},
	}
	stronger := map[string]int{"mechanical": 1, "implementation": 2}
	covers := func(a, b string) bool {
		return a == b || stronger[a] > 0 && stronger[b] > 0 && stronger[a] >= stronger[b]
	}
	lim := Limits{Repos: map[string]RepoLimits{"vex": {Sessions: 1}}, Covers: covers}
	got := Next([]*Goal{g}, nil, lim)
	if len(got) != 1 || !slices.Equal(ids(got[0]), []string{"1", "2", "3"}) ||
		got[0].Profile != "implementation" {
		t.Fatalf("Next = %+v, want 1, 2, 3 on implementation", got)
	}
	lim.Covers = nil
	if got := Next(
		[]*Goal{g},
		nil,
		lim,
	); len(got) != 1 ||
		!slices.Equal(ids(got[0]), []string{"1"}) ||
		got[0].Profile != "mechanical" {
		t.Errorf("Next without Covers = %+v, want 1 alone on mechanical", got)
	}
}
