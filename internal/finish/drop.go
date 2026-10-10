package finish

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// Drop gives up on a goal: none of its work lands. Its tasks are closed,
// its questions and notes answered for, and its worktrees and branches go
// as they do when a goal finishes. The goal's own files stay, so its
// sessions and what they cost are still counted.
func Drop(ctx context.Context, s *queue.Store, g *queue.Goal, why string, now time.Time) error {
	if g.Over() {
		return fmt.Errorf("goal %s is already %s", g.Name, g.State)
	}
	if err := closeWork(s, g, why); err != nil {
		return err
	}
	g.State, g.Finished, g.Reason = queue.GoalDropped, now, why
	if err := s.SaveGoal(g); err != nil {
		return err
	}
	return errors.Join(RemoveWorktrees(ctx, s, g), removeBranches(ctx, s, g))
}

// closeWork ends everything of a dropped goal that still waits on someone:
// its tasks, the questions they parked on, and the notes left to read.
func closeWork(s *queue.Store, g *queue.Goal, why string) error {
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.State == queue.Done {
			continue
		}
		if err := s.AppendNote(g.Name, t.ID, "Dropped", why); err != nil {
			return err
		}
		t, err := s.Task(g.Name, t.ID)
		if err != nil {
			return err
		}
		if err := s.Move(g.Name, t, queue.Done); err != nil {
			return err
		}
	}
	qs, err := s.Questions(g.Name, queue.QuestionOpen)
	if err != nil {
		return err
	}
	for _, q := range qs {
		if err := s.CloseQuestion(g.Name, q); err != nil {
			return err
		}
	}
	notes, err := s.Notes(g.Name, queue.NoteOpen)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if err := s.ReadNote(g.Name, n); err != nil {
			return err
		}
	}
	return nil
}

// removeBranches deletes a dropped goal's branches, whose commits will
// never land. A branch that is gone already is left alone.
func removeBranches(ctx context.Context, s *queue.Store, g *queue.Goal) error {
	repo := git.Repo{Dir: s.Repo()}
	branches := []string{g.IntegrationBranch()}
	for _, ws := range g.Workstreams {
		branches = append(branches, g.WorkstreamBranch(ws.Name))
	}
	var errs []error
	for _, b := range branches {
		if !repo.BranchExists(ctx, b) {
			continue
		}
		if _, err := repo.Run(ctx, "branch", "-D", b); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
