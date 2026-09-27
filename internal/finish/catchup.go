package finish

import (
	"context"
	"fmt"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// BaseTip is what the goal lands on: its base branch, or the upstream branch
// the base follows when that is ahead, as it is before the human pulls.
func BaseTip(ctx context.Context, repo git.Repo, g *queue.Goal) string {
	up := repo.Upstream(ctx, g.Base)
	if up == "" {
		return g.Base
	}
	if ahead, err := repo.IsAncestor(ctx, g.Base, up); err != nil || !ahead {
		return g.Base
	}
	return up
}

// CatchUp brings the goal's integration branch up to date with its base
// branch, fetched from remote first. A merge that goes through cleanly is
// made at once. One that conflicts goes to an agent: a conflict task merges
// the base into the goal's last workstream, and the goal is active again until
// the task is done and its resolution reviewed. CatchUp reports whether the
// integration branch is up to date.
func CatchUp(
	ctx context.Context,
	s *queue.Store,
	g *queue.Goal,
	remote string,
	now time.Time,
) (bool,
	error,
) {
	repo := git.Repo{Dir: s.Repo()}
	if remote != "" {
		// Offline, the goal catches up with what was fetched last.
		_, _ = repo.Run(ctx, "fetch", "--quiet", remote, g.Base)
	}
	tip := BaseTip(ctx, repo, g)
	conflicted, err := repo.MergeInto(ctx, g.IntegrationBranch(), tip)
	if err != nil || !conflicted {
		return err == nil, err
	}
	order := workstreamOrder(g)
	ws := order[len(order)-1]
	if ws == "" {
		return false, fmt.Errorf(
			"%s has moved on and conflicts with goal %s, which has no workstream "+
				"to resolve it in",
			g.Base,
			g.Name,
		)
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return false, err
	}
	queued := false
	for _, t := range tasks {
		queued = queued || t.Kind == queue.Conflict && t.Merge != "" && t.State != queue.Done
	}
	if !queued {
		if err := s.AddTask(g.Name, &queue.Task{
			Title:      "Merge " + g.Base + " into the goal",
			Kind:       queue.Conflict,
			Workstream: ws,
			Merge:      tip,
			Origin:     queue.Origin{Type: "harness"},
			Created:    now,
			Body: fmt.Sprintf(
				"%s has moved on since this goal started, and merging it into the goal "+
					"conflicts. diatom merges %s into the worktree when this task starts, after the integration "+
					"branch: resolve every conflicted file, keeping the intent of both sides. Your resolution "+
					"is reviewed before the goal lands.",
				g.Base,
				tip,
			),
		}); err != nil {
			return false, err
		}
	}
	if g.State == queue.GoalDone {
		g.State = queue.GoalActive
		return false, s.SaveGoal(g)
	}
	return false, nil
}
