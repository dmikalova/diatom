package finish

import (
	"context"
	"fmt"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
)

// MarkDone ends a goal's work, but first refuses while hunks nobody has
// decided on, deferred ones included (ADR 0001), or tasks not done are left;
// force ends it anyway. A goal a session is still working on is never done.
func MarkDone(ctx context.Context, s *queue.Store, g *queue.Goal, force bool) error {
	if g.State == queue.GoalDone || g.State == queue.GoalFinished {
		return fmt.Errorf("goal %s is already %s", g.Name, g.State)
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return err
	}
	open := 0
	for _, t := range tasks {
		switch t.State {
		case queue.Active:
			return fmt.Errorf("a session is working on task %s of goal %s: park the goal "+
				"and let the session end first", t.ID, g.Name)
		case queue.Pending, queue.Blocked:
			open++
		}
	}
	items, err := review.Load(ctx, s, g.Name)
	if err != nil {
		return err
	}
	counts := review.Counts(items)
	if left := counts[""] + counts[review.Defer]; (left > 0 || open > 0) && !force {
		return fmt.Errorf(
			"goal %s has %d unreviewed and %d deferred hunks and %d tasks not done: review the hunks, "+
				"or finish it anyway with -force",
			g.Name,
			counts[""],
			counts[review.Defer],
			open,
		)
	}
	g.State = queue.GoalDone
	return s.SaveGoal(g)
}
