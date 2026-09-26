package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// planningReport is the task tool of triage and grilling sessions:
//
//	diatom task add-task <id> -goal <goal> -ws <workstream> -title <title> [-after ids] [-profile p] < body
//	diatom task feedback <id> -goal <goal> < text
//	diatom task new-goal <id> -title <title> [-plan plan.yaml] < description
//	diatom task plan <id> < plan.yaml
func planningReport(sub string, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	goal := fs.String("goal", "", "the goal")
	ws := fs.String("ws", "", "the workstream")
	planFile := fs.String("plan", "", "the new goal's plan, when its work is already decided")
	title := fs.String("title", "", "the title")
	after := fs.String("after", "", "ids of tasks that must be done first, comma-separated")
	profile := fs.String("profile", "", "the profile, instead of the kind's default")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("%w: task %s takes the id of your task", errUsage, sub)
	}
	dir, spec, err := session.FromEnv()
	if err != nil {
		return err
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	e := session.Entry{Task: pos[0], Text: string(bytes.TrimSpace(body))}
	switch sub {
	case "add-task", "feedback", "new-goal":
		if spec.Kind != queue.Triage {
			return fmt.Errorf("task %s is only for triage sessions", sub)
		}
		if err := triageEntry(
			&e,
			sub,
			*goal,
			*title,
			*ws,
			*after,
			*profile,
			*planFile,
		); err != nil {
			return err
		}
	case "plan":
		if spec.Kind != queue.Grilling {
			return fmt.Errorf("task plan is only for grilling sessions")
		}
		if _, err := plan.Parse([]byte(e.Text)); err != nil {
			return fmt.Errorf("the plan was not accepted; fix it and hand it in again:\n%w", err)
		}
		e.Type = session.EntryPlan
	}
	if err := session.Append(dir, spec, e); err != nil {
		return err
	}
	switch e.Type {
	case session.EntryAdd:
		_, _ = fmt.Fprintf(
			stdout,
			"Task %q will be added to goal %s, workstream %s.\n",
			e.Title,
			e.Goal,
			e.Workstream,
		)
	case session.EntryFeedback:
		_, _ = fmt.Fprintf(stdout, "The feedback will go to goal %s's grilling.\n", e.Goal)
	case session.EntryGoal:
		if e.Plan != "" {
			_, _ = fmt.Fprintf(
				stdout,
				"A new goal %q will be started with its plan, for the human "+
					"to sign off.\n",
				e.Title,
			)
			break
		}
		_, _ = fmt.Fprintf(
			stdout,
			"A new goal %q will be started for the human to grill.\n",
			e.Title,
		)
	default:
		_, _ = fmt.Fprintln(stdout, "The plan is accepted and will go to the human for sign-off. "+
			"Mark the task done and end the session.")
	}
	return nil
}

// triageEntry fills in what a triage session hands in with add-task,
// feedback or new-goal.
func triageEntry(e *session.Entry, sub, goal, title, ws, after, profile, planFile string) error {
	switch sub {
	case "feedback":
		if goal == "" || e.Text == "" {
			return fmt.Errorf("%w: task feedback needs -goal, and the feedback on stdin", errUsage)
		}
		e.Type, e.Goal = session.EntryFeedback, goal
		return nil
	case "add-task":
		if goal == "" || ws == "" || strings.TrimSpace(title) == "" {
			return fmt.Errorf("%w: task add-task needs -goal, -ws and -title", errUsage)
		}
		e.Type, e.Goal, e.Title, e.Workstream, e.Profile = session.EntryAdd, goal, title, ws, profile
		for id := range strings.SplitSeq(after, ",") {
			if id = strings.TrimSpace(id); id != "" {
				e.After = append(e.After, id)
			}
		}
		return nil
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%w: task new-goal needs -title", errUsage)
	}
	e.Type, e.Title = session.EntryGoal, title
	if planFile == "" {
		return nil
	}
	b, err := os.ReadFile(planFile)
	if err != nil {
		return err
	}
	if _, err := plan.Parse(b); err != nil {
		return fmt.Errorf("the plan was not accepted; fix it and hand it in again:\n%w", err)
	}
	e.Plan = string(b)
	return nil
}
