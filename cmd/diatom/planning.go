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

// planningReport is the task tool of triage and grilling sessions, and
// new-goal of every session's:
//
//	diatom task add-task <id> -goal <goal> -ws <workstream> -title <title> [-after ids] [-profile p] < body
//	diatom task feedback <id> -goal <goal> < text
//	diatom task new-goal <id> -title <title> -description <line> [-after goals] [-plan plan.yaml] < brief
//	diatom task after <id> -goal <goal> [-after goals]
//	diatom task plan <id> < plan.yaml
func planningReport(sub string, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	goal := fs.String("goal", "", "the goal")
	ws := fs.String("ws", "", "the workstream")
	planFile := fs.String("plan", "", "the new goal's plan, when its work is already decided")
	title := fs.String("title", "", "the title")
	description := fs.String("description", "", "what a new goal is for, in a line")
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
	case "add-task", "after", "feedback", "new-goal":
		// Any session may start a goal the human asked for; sorting work
		// into goals is triage's alone.
		if spec.Kind != queue.Triage && sub != "new-goal" {
			return fmt.Errorf("task %s is only for triage sessions", sub)
		}
		if err := triageEntry(&e, sub, entryFlags{
			goal: *goal, title: *title, description: *description, ws: *ws,
			after: *after, profile: *profile, planFile: *planFile,
		}); err != nil {
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
	case session.EntryAfter:
		_, _ = fmt.Fprintf(stdout, "Goal %s will wait for %s.\n", e.Goal, orNone(e.After))
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

// entryFlags are the flags of what a triage session hands in.
type entryFlags struct {
	goal, title, description, ws, after, profile, planFile string
}

// triageEntry fills in what a triage session hands in with add-task,
// feedback or new-goal.
func triageEntry(e *session.Entry, sub string, f entryFlags) error {
	goal, title, ws, after, profile := f.goal, f.title, f.ws, f.after, f.profile
	switch sub {
	case "feedback":
		if goal == "" || e.Text == "" {
			return fmt.Errorf("%w: task feedback needs -goal, and the feedback on stdin", errUsage)
		}
		e.Type, e.Goal = session.EntryFeedback, goal
		return nil
	case "after":
		if goal == "" {
			return fmt.Errorf(
				"%w: task after needs -goal, and -after with the goals it waits for",
				errUsage,
			)
		}
		e.Type, e.Goal, e.After = session.EntryAfter, goal, splitList(after)
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
	description := strings.TrimSpace(f.description)
	if strings.TrimSpace(title) == "" || description == "" {
		return fmt.Errorf("%w: task new-goal needs -title and -description", errUsage)
	}
	if strings.Contains(description, "\n") {
		return fmt.Errorf("%w: -description is one line; the rest goes on stdin", errUsage)
	}
	e.Type, e.Title, e.Description, e.After = session.EntryGoal, title, description, splitList(
		after,
	)
	if f.planFile == "" {
		return nil
	}
	b, err := os.ReadFile(f.planFile)
	if err != nil {
		return err
	}
	if _, err := plan.Parse(b); err != nil {
		return fmt.Errorf("the plan was not accepted; fix it and hand it in again:\n%w", err)
	}
	e.Plan = string(b)
	return nil
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "no other goal"
	}
	return strings.Join(names, " and ")
}
