// Package schedule decides which work starts next (ADR 0004). It is a pure
// function over a snapshot of the queues, so the scheduler's loop only has to
// gather the snapshot and start what comes back.
//
// Ready tasks run in a strict priority order: gate repair, then conflict
// resolution, then revisions, then planned work. A session takes a batch: as
// many ready tasks as fit, of the same kind, profile and effort, in the same
// workstream. A workstream runs one session at a time, because a session owns
// its worktree.
package schedule

import (
	"cmp"
	"slices"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
)

// rank orders the task kinds, first to run first, as ADR 0004 records.
var rank = map[queue.Kind]int{
	queue.GateRepair: 0,
	queue.Conflict:   1,
	queue.Revision:   2,
	queue.Triage:     3,
	queue.Grilling:   4,
	queue.Planned:    5,
}

// Rank is a kind's place in the priority order, lowest first.
func Rank(k queue.Kind) int {
	if r, ok := rank[k]; ok {
		return r
	}
	return len(rank)
}

// Ready returns a goal's pending tasks whose dependencies are all done.
func Ready(tasks []*queue.Task) []*queue.Task {
	done := map[string]bool{}
	for _, t := range tasks {
		if t.State == queue.Done {
			done[t.ID] = true
		}
	}
	var ready []*queue.Task
	for _, t := range tasks {
		if t.State == queue.Pending &&
			!slices.ContainsFunc(t.DependsOn, func(id string) bool { return !done[id] }) {
			ready = append(ready, t)
		}
	}
	return ready
}

// Goal is one goal's share of the snapshot.
type Goal struct {
	Repo    string
	Name    string
	Pinned  bool
	Created time.Time
	// Ready are the goal's ready tasks, from Ready.
	Ready []*queue.Task
}

// Running is a session already in progress.
type Running struct {
	Repo, Goal, Workstream string
}

// Limits caps how much runs at once.
type Limits struct {
	// Repos are each repo's own caps; a repo missing from it gets one
	// session and uncapped batches.
	Repos map[string]RepoLimits
	// Machine caps the sessions across every repo; 0 is no cap.
	Machine int
}

// RepoLimits are one repo's caps.
type RepoLimits struct {
	// Sessions caps the sessions running in the repo at once.
	Sessions int
	// Batch caps the tasks in one batch; 0 is no cap.
	Batch int
}

// Batch is a set of tasks for one agent session.
type Batch struct {
	Repo, Goal, Workstream string
	Kind                   queue.Kind
	Profile                string
	// Effort overrides the profile's effort level when its tasks are retries.
	Effort string
	Tasks  []*queue.Task
}

type candidate struct {
	goal *Goal
	task *queue.Task
}

// Next returns the batches to start now, given the ready work and the
// sessions already running. Pinned goals come first, then the priority order,
// then the older goal, then the task's own priority and id.
func Next(goals []*Goal, running []Running, lim Limits) []Batch {
	var cands []candidate
	for _, g := range goals {
		for _, t := range g.Ready {
			cands = append(cands, candidate{g, t})
		}
	}
	slices.SortStableFunc(cands, compare)

	type wsKey struct{ repo, goal, ws string }
	busy := map[wsKey]bool{}
	perRepo := map[string]int{}
	for _, r := range running {
		busy[wsKey{r.Repo, r.Goal, r.Workstream}] = true
		perRepo[r.Repo]++
	}
	total := len(running)

	var batches []Batch
	taken := map[*queue.Task]bool{}
	for _, c := range cands {
		key := wsKey{c.goal.Repo, c.goal.Name, c.task.Workstream}
		rl := repoLimits(lim, key.repo)
		if taken[c.task] || busy[key] || perRepo[key.repo] >= rl.Sessions ||
			(lim.Machine > 0 && total >= lim.Machine) {
			continue
		}
		b := Batch{
			Repo:       key.repo,
			Goal:       key.goal,
			Workstream: key.ws,
			Kind:       c.task.Kind,
			Profile:    c.task.Profile,
			Effort:     c.task.Effort,
		}
		for _, o := range cands {
			if rl.Batch > 0 && len(b.Tasks) == rl.Batch {
				break
			}
			if o.goal == c.goal && o.task.Workstream == key.ws && o.task.Kind == b.Kind &&
				o.task.Profile == b.Profile && o.task.Effort == b.Effort && !taken[o.task] {
				b.Tasks = append(b.Tasks, o.task)
				taken[o.task] = true
			}
		}
		batches = append(batches, b)
		busy[key] = true
		perRepo[key.repo]++
		total++
	}
	return batches
}

func repoLimits(lim Limits, repo string) RepoLimits {
	if rl, ok := lim.Repos[repo]; ok {
		return rl
	}
	return RepoLimits{Sessions: 1}
}

func compare(a, b candidate) int {
	if a.goal.Pinned != b.goal.Pinned {
		if a.goal.Pinned {
			return -1
		}
		return 1
	}
	return cmp.Or(
		cmp.Compare(Rank(a.task.Kind), Rank(b.task.Kind)),
		a.goal.Created.Compare(b.goal.Created),
		cmp.Compare(a.goal.Repo+"/"+a.goal.Name, b.goal.Repo+"/"+b.goal.Name),
		cmp.Compare(a.task.Priority, b.task.Priority),
		cmp.Compare(a.task.ID, b.task.ID),
	)
}
