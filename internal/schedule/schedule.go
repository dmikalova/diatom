// Package schedule decides which work starts next (ADR 0004). It is a pure
// function over a snapshot of the queues, so the scheduler's loop only has to
// gather the snapshot and start what comes back.
//
// Ready tasks run in a strict priority order: gate repair, then conflict
// resolution, then revisions, then planned work. A session takes a batch: as
// many ready tasks as fit, of the same kind, profile and effort, in the same
// workstream, then the chain of tasks that wait only on them there, the
// session moving up to the more capable profile a task of the chain needs. A
// workstream runs one session at a time, because a session owns its worktree.
//
// Work the harness does without an agent, such as merging a goal's base into
// a workstream, runs beside the sessions: it takes its workstream but no
// session, so it never waits for an agent's slot.
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

// Ready returns a goal's pending tasks whose dependencies are all done and
// which are not waiting for a commit's checks (ADR 0014).
func Ready(tasks []*queue.Task) []*queue.Task {
	done := map[string]bool{}
	for _, t := range tasks {
		if t.State == queue.Done {
			done[t.ID] = true
		}
	}
	var ready []*queue.Task
	for _, t := range tasks {
		if t.State == queue.Pending && t.CI == "" &&
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
	Created time.Time
	// Ready are the goal's ready tasks, from Ready.
	Ready []*queue.Task
	// Later are its pending tasks that wait on others, and Unfinished holds
	// the IDs of every task not done. A session that takes a task can go on
	// to one that waits only on it, in the same workstream.
	Later      []*queue.Task
	Unfinished map[string]bool
	// Mechanical are its ready tasks the harness tries without an agent
	// first: conflict tasks whose merges haven't been tried in the
	// workstream yet.
	Mechanical []*queue.Task
}

// Running is a session, or mechanical work, already in progress.
type Running struct {
	Repo, Goal, Workstream string
	// Mechanical is work without an agent, which takes no session.
	Mechanical bool
}

// Limits caps how much runs at once.
type Limits struct {
	// Repos are each repo's own caps; a repo missing from it gets one
	// session and uncapped batches.
	Repos map[string]RepoLimits
	// Machine caps the sessions across every repo given; 0 is no cap. Every
	// repo in Repos keeps one of those sessions to itself.
	Machine int
	// Covers reports whether profile a can do profile b's work, so a chain
	// may go on from one to the other on a. Nil covers only a profile itself.
	Covers func(a, b string) bool
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
	// Mechanical is a batch the harness runs without an agent, from
	// Goal.Mechanical.
	Mechanical bool
}

// wsKey is a workstream of a goal in a repo.
type wsKey struct{ repo, goal, ws string }

type candidate struct {
	goal *Goal
	task *queue.Task
}

// Next returns the batches to start now, given the ready work and the
// sessions already running: the mechanical batches first, in every
// workstream not busy, and taking no session, then the priority order, then the older goal,
// then the task's own priority and id. A batch goes on, in order, to tasks
// that wait only on tasks done or already in it, so a chain of dependent
// tasks in a workstream runs in one session, on the most capable profile
// the chain needs.
func Next(goals []*Goal, running []Running, lim Limits) []Batch {
	var cands []candidate
	for _, g := range goals {
		for _, t := range g.Ready {
			cands = append(cands, candidate{g, t})
		}
	}
	slices.SortStableFunc(cands, compare)

	busy := map[wsKey]bool{}
	perRepo := map[string]int{}
	total := 0
	for _, r := range running {
		busy[wsKey{r.Repo, r.Goal, r.Workstream}] = true
		if !r.Mechanical {
			perRepo[r.Repo]++
			total++
		}
	}

	batches := mechanical(goals, busy)
	taken := map[*queue.Task]bool{}
	for _, c := range cands {
		key := wsKey{c.goal.Repo, c.goal.Name, c.task.Workstream}
		rl := repoLimits(lim, key.repo)
		if taken[c.task] || busy[key] || perRepo[key.repo] >= rl.Sessions ||
			!machineRoom(lim, perRepo, total, key.repo) {
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
		chain(&b, c.goal, taken, rl.Batch, lim.Covers)
		batches = append(batches, b)
		busy[key] = true
		perRepo[key.repo]++
		total++
	}
	return batches
}

// mechanical returns a batch for each workstream not busy of the goals'
// mechanical tasks there, and marks those workstreams busy.
func mechanical(goals []*Goal, busy map[wsKey]bool) []Batch {
	var batches []Batch
	at := map[wsKey]int{}
	for _, g := range goals {
		for _, t := range g.Mechanical {
			key := wsKey{g.Repo, g.Name, t.Workstream}
			if i, ok := at[key]; ok {
				batches[i].Tasks = append(batches[i].Tasks, t)
				continue
			}
			if busy[key] {
				continue
			}
			at[key] = len(batches)
			batches = append(batches, Batch{
				Repo: g.Repo, Goal: g.Name, Workstream: t.Workstream, Kind: t.Kind,
				Tasks: []*queue.Task{t}, Mechanical: true,
			})
		}
	}
	for key := range at {
		busy[key] = true
	}
	return batches
}

func repoLimits(lim Limits, repo string) RepoLimits {
	if rl, ok := lim.Repos[repo]; ok {
		return rl
	}
	return RepoLimits{Sessions: 1}
}

// machineRoom reports whether the machine's cap leaves repo a session. Every
// repo of the workspace keeps one slot to itself, so a busy repo never
// starves a quiet one: a repo with a session running takes another only
// while there are more free slots than there are repos still waiting for
// their own.
func machineRoom(lim Limits, perRepo map[string]int, total int, repo string) bool {
	if lim.Machine <= 0 {
		return true
	}
	free := lim.Machine - total
	if free <= 0 {
		return false
	}
	if perRepo[repo] == 0 {
		return true
	}
	idle := 0
	for r := range lim.Repos {
		if r != repo && perRepo[r] == 0 {
			idle++
		}
	}
	return free > idle
}

// chain adds to b, one at a time and in priority order, the goal's later
// tasks whose dependencies are all done or in b, up to size tasks. A task on
// another profile joins when one of the two profiles covers the other, and
// the batch runs on the one that does: a second session would read its
// instructions and the code all over again, costing more than the cheaper
// model saves on a task of the chain.
func chain(b *Batch, g *Goal, taken map[*queue.Task]bool, size int, covers func(a, b string) bool) {
	if covers == nil {
		covers = func(a, b string) bool { return a == b }
	}
	later := slices.Clone(g.Later)
	slices.SortStableFunc(later, func(x, y *queue.Task) int {
		return cmp.Or(cmp.Compare(x.Priority, y.Priority), cmp.Compare(x.ID, y.ID))
	})
	in := map[string]bool{}
	for _, t := range b.Tasks {
		in[t.ID] = true
	}
	for size == 0 || len(b.Tasks) < size {
		i := slices.IndexFunc(later, func(t *queue.Task) bool {
			return !taken[t] && t.Workstream == b.Workstream && t.Kind == b.Kind &&
				(covers(b.Profile, t.Profile) || covers(t.Profile, b.Profile)) &&
				t.Effort == b.Effort &&
				!slices.ContainsFunc(
					t.DependsOn,
					func(d string) bool { return g.Unfinished[d] && !in[d] },
				)
		})
		if i < 0 {
			return
		}
		t := later[i]
		if !covers(b.Profile, t.Profile) {
			b.Profile = t.Profile
		}
		b.Tasks = append(b.Tasks, t)
		taken[t], in[t.ID] = true, true
	}
}

func compare(a, b candidate) int {
	return cmp.Or(
		cmp.Compare(Rank(a.task.Kind), Rank(b.task.Kind)),
		a.goal.Created.Compare(b.goal.Created),
		cmp.Compare(a.goal.Repo+"/"+a.goal.Name, b.goal.Repo+"/"+b.goal.Name),
		cmp.Compare(a.task.Priority, b.task.Priority),
		cmp.Compare(a.task.ID, b.task.ID),
	)
}
