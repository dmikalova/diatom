package harness

import (
	"context"
	"slices"
	"time"

	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// fetchEvery is how often the scheduler fetches the goals' base branches, for
// what lands upstream by other hands, such as a pull request merged on
// GitHub. What diatom pushes itself moves the remote-tracking branch at once,
// so the next pass sees it.
const fetchEvery = 2 * time.Minute

// followBases merges what each goal's base branch gained into its integration
// branch on the pass it shows up, so no session starts on a base that has
// moved on (ADR 0003). The integration branch is only a ref: each workstream
// takes it in before its next batch, as always, so a running session is left
// to finish first. A merge that conflicts goes to an agent as a conflict task,
// as catching up before landing does, and a done goal is active again until
// it is resolved.
func (h *Harness) followBases(ctx context.Context, s *queue.Store, goals []*queue.Goal) {
	main := git.Repo{Dir: s.Repo()}
	var follow []*queue.Goal
	for _, g := range goals {
		if h.follows(ctx, s, main, g) {
			follow = append(follow, g)
		}
	}
	h.fetchBases(ctx, main, follow)
	for _, g := range follow {
		tip := finish.BaseTip(ctx, main, g)
		if in, err := main.IsAncestor(ctx, tip, g.IntegrationBranch()); err != nil || in {
			continue
		}
		unlock := h.lockRepo(main.Dir)
		up, err := finish.CatchUp(ctx, s, g, "", h.now())
		unlock()
		if err != nil {
			h.log().Warn("taking in what a goal's base gained failed", "goal", g.Name, "base", tip,
				"err", err)
			continue
		}
		h.log().Info("a goal took in what its base gained", "goal", g.Name, "base", tip,
			"clean", up)
	}
}

// follows reports whether a goal takes in what its base gains as it comes: a
// goal at work, parked or done, with an integration branch. A goal still
// planning takes its base in once it starts, a waiting one as it stops
// waiting, and one pushed or opened upstream has landed what it holds. One
// with a catch-up already queued takes the base in when that task runs.
func (h *Harness) follows(ctx context.Context, s *queue.Store, main git.Repo, g *queue.Goal) bool {
	if g.Name == queue.IntakeGoal || g.Base == "" ||
		!slices.Contains([]queue.GoalState{queue.GoalActive, queue.GoalParked, queue.GoalDone},
			g.State) ||
		len(s.Waiting(g)) > 0 || !main.BranchExists(ctx, g.IntegrationBranch()) {
		return false
	}
	if g.State == queue.GoalDone {
		if res, err := finish.Load(s.GoalDir(g.Name)); err != nil ||
			res != nil && res.Landing != nil && res.Landing.How != "" {
			return false
		}
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return false
	}
	return !slices.ContainsFunc(tasks, func(t *queue.Task) bool {
		return t.Kind == queue.Conflict && t.Merge != "" && t.State != queue.Done
	})
}

// fetchBases fetches the goals' base branches from origin, at most once every
// fetchEvery. Offline, the goals follow what was fetched last.
func (h *Harness) fetchBases(ctx context.Context, main git.Repo, goals []*queue.Goal) {
	if len(goals) == 0 || h.now().Sub(h.fetched) < fetchEvery {
		return
	}
	h.fetched = h.now()
	ctx, cancel := context.WithTimeout(ctx, watchTimeout)
	defer cancel()
	var bases []string
	for _, g := range goals {
		if !slices.Contains(bases, g.Base) {
			bases = append(bases, g.Base)
			_, _ = main.Run(ctx, "fetch", "--quiet", "origin", g.Base)
		}
	}
}
