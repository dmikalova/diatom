package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/reviewui"
)

// cmdReview opens the reviewer on a goal, or with -list prints what is left
// to review.
func cmdReview(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	goal := fs.String("goal", "", "the goal to review; defaults to the repo's only open goal")
	list := fs.Bool("list", false, "print the hunks left to review instead of opening the reviewer")
	follow := fs.Bool("focus", false, "review the focused goal, following the status pane")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if *follow {
		fc, err := focus.Default()
		if err != nil {
			return err
		}
		return runProgram(ctx, reviewui.NewFollow(ctx, fc))
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	name, err := reviewGoal(s, *goal)
	if err != nil {
		return err
	}
	if *list {
		items, err := review.Load(ctx, s, name)
		if err != nil {
			return err
		}
		for _, it := range review.Pending(items) {
			state := "unreviewed"
			if it.Record != nil {
				state = string(it.Record.Decision)
			}
			_, _ = fmt.Fprintf(stdout, "%s %s %s (%s)\n", it.Commit[:7], it.ID, it.Subject, state)
		}
		return nil
	}
	m, err := reviewui.New(ctx, s, name)
	if err != nil {
		return err
	}
	return runProgram(ctx, m)
}

// runProgram runs a terminal UI until it quits or ctx ends.
func runProgram(ctx context.Context, m tea.Model) error {
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

// reviewGoal picks the goal to review: the named one, or the repo's only
// goal that isn't done.
func reviewGoal(s *queue.Store, name string) (string, error) {
	if name != "" {
		_, err := s.Goal(name)
		return name, err
	}
	goals, err := s.Goals()
	if err != nil {
		return "", err
	}
	var open []string
	for _, g := range goals {
		if g.State != queue.GoalDone {
			open = append(open, g.Name)
		}
	}
	switch len(open) {
	case 0:
		return "", errors.New("no open goal to review in this repo")
	case 1:
		return open[0], nil
	}
	return "", fmt.Errorf("%w: pick a goal with -goal: %s", errUsage, strings.Join(open, ", "))
}
