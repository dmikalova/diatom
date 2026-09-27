package finish

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
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

// GateError is a goal whose prepared commits fail the gate, so it doesn't
// land.
type GateError struct {
	Goal, Branch, Output string
}

func (e *GateError) Error() string {
	return fmt.Sprintf("goal %s fails the gate on %s", e.Goal, e.Branch)
}

// Failing is the prepared goal's failing gate on its last commit, nil when
// it passes or wasn't run.
func (r *Result) Failing() *GateError {
	last := r.Stack[len(r.Stack)-1]
	if last.Gate == nil || last.Gate.Passed {
		return nil
	}
	return &GateError{Branch: last.Branch, Output: last.Gate.Output}
}

// RepairGate hands a goal whose prepared commits fail the gate to an agent:
// a gate-repair task on its last workstream, with the gate's output, and the
// goal active again until it is done. Landing never forces past the gate.
func RepairGate(s *queue.Store, g *queue.Goal, failed *GateError, now time.Time) error {
	order := workstreamOrder(g)
	ws := order[len(order)-1]
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("The goal's commits, put on %s's tip as %s to land, fail the gate. Make "+
		"it pass in this workstream, which holds everything the goal has done.\n\n```text\n%s\n```",
		g.Base, failed.Branch, strings.TrimSpace(failed.Output))
	queued := false
	for _, t := range tasks {
		if t.Kind == queue.GateRepair && t.Workstream == ws && t.State != queue.Done {
			t.Body += "\n\n## Landing again\n\n" + body
			if err := s.SaveTask(g.Name, t); err != nil {
				return err
			}
			queued = true
		}
	}
	if !queued {
		if err := s.AddTask(g.Name, &queue.Task{
			Title:      "Make the goal pass the gate on " + g.Base,
			Kind:       queue.GateRepair,
			Workstream: ws,
			Origin:     queue.Origin{Type: "harness"},
			Created:    now,
			Body:       body,
		}); err != nil {
			return err
		}
	}
	if g.State == queue.GoalDone {
		g.State = queue.GoalActive
		return s.SaveGoal(g)
	}
	return nil
}

// recordSettlements puts a landing's settlements up for review: a task, done
// from the start, whose commits are just what the agent changed.
func recordSettlements(s *queue.Store, g *queue.Goal, res *Result, now time.Time) error {
	if len(res.Settlements) == 0 {
		return nil
	}
	order := workstreamOrder(g)
	t := &queue.Task{
		Title:      "Review how an agent settled " + g.Base + "'s conflicts",
		Kind:       queue.Conflict,
		Workstream: order[len(order)-1],
		Origin:     queue.Origin{Type: landingOrigin},
		Created:    now,
		Body: fmt.Sprintf(
			"Landing rebased the goal's commits onto %s, which changed the same code, and an "+
				"agent settled the conflicts git left. Each commit here holds only the agent's change, from the "+
				"files as git left them to the files as it settled them. The goal lands once they are "+
				"approved; rejecting one settles that commit again, with the comments.",
			g.Base,
		),
	}
	for _, st := range res.Settlements {
		t.Commits = append(t.Commits, st.Review)
	}
	return s.AddDone(g.Name, t)
}

// landingOrigin marks the tasks a landing adds for review, whose rejections
// the landing handles rather than a revision.
const landingOrigin = "landing"

// IsLanding reports whether a task holds a landing's settlements.
func IsLanding(t *queue.Task) bool { return t.Origin.Type == landingOrigin }

// Settled says how far the human has reviewed a layout's settlements: whether
// any hunk waits, and for each commit whose settlement they rejected, what
// they said.
func Settled(ctx context.Context, s *queue.Store, g *queue.Goal, res *Result) (bool,
	map[string]string, error,
) {
	repo := git.Repo{Dir: s.Repo()}
	store := review.Store{Dir: s.GoalDir(g.Name)}
	waiting, feedback := false, map[string]string{}
	for _, st := range res.Settlements {
		hunks, err := review.Hunks(ctx, repo, st.Review)
		if err != nil {
			return false, nil, err
		}
		rec, err := store.Load(st.Review)
		if err != nil {
			return false, nil, err
		}
		var said []string
		for _, h := range hunks {
			r := rec.Hunks[h.ID]
			switch {
			case r == nil || r.Decision == review.Defer:
				waiting = true
			case r.Decision == review.Reject:
				said = append(said, "- "+h.Path+": rejected")
				for _, c := range r.Comments {
					said = append(said, "  - "+c.Text)
				}
			}
		}
		if len(said) > 0 {
			feedback[st.Commit] = strings.Join(said, "\n")
		}
	}
	return waiting, feedback, nil
}

// feedbackFile keeps the human's comments on rejected settlements until the
// goal lands.
const feedbackFile = "settle-feedback.yaml"

// SaveFeedback keeps the comments on rejected settlements for the next
// landing, and Discard throws the layout away so it is made again.
func SaveFeedback(goalDir string, feedback map[string]string) error {
	if len(feedback) == 0 {
		err := os.Remove(filepath.Join(goalDir, feedbackFile))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := yaml.Marshal(feedback)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(goalDir, feedbackFile), b, 0o644)
}

// LoadFeedback is the comments on rejected settlements, if any.
func LoadFeedback(goalDir string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(goalDir, feedbackFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var feedback map[string]string
	return feedback, yaml.Unmarshal(b, &feedback)
}

// Discard throws a goal's layout away, to be made again.
func Discard(goalDir string) error {
	err := os.Remove(filepath.Join(goalDir, fileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// MoreWork adds what the human says a goal ready to finish still needs as a
// task of its own, on the workstream that lands last, and makes the goal
// active again: it finishes once that work is done and reviewed.
func MoreWork(s *queue.Store, g *queue.Goal, text string, now time.Time) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("say what more the goal needs")
	}
	order := workstreamOrder(g)
	ws := order[len(order)-1]
	if ws == "" {
		return fmt.Errorf("goal %s has no workstream to do more work in", g.Name)
	}
	title, _, _ := strings.Cut(text, "\n")
	if len([]rune(title)) > 72 {
		title = string([]rune(title)[:71]) + "…"
	}
	if err := s.AddTask(g.Name, &queue.Task{
		Title:      title,
		Kind:       queue.Planned,
		Workstream: ws,
		Origin:     queue.Origin{Type: "finish"},
		Created:    now,
		Body: "Before the goal lands, the human asked for more work on it:\n\n" + text + "\n\n" +
			"Do this on top of what the goal has done so far.",
	}); err != nil {
		return err
	}
	if g.State == queue.GoalDone {
		g.State = queue.GoalActive
		return s.SaveGoal(g)
	}
	return nil
}
