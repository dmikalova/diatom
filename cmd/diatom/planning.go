package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// planningReport is the task tool of triage and grilling sessions:
//
//	diatom task add-task <id> -ws <workstream> -title <title> [-after ids] [-profile p] < body
//	diatom task new-goal <id> -title <title> < description
//	diatom task plan <id> < plan.yaml
func planningReport(sub string, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ws := fs.String("ws", "", "the workstream")
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
	case "add-task", "new-goal":
		if spec.Kind != queue.Triage {
			return fmt.Errorf("task %s is only for triage sessions", sub)
		}
		if strings.TrimSpace(*title) == "" {
			return fmt.Errorf("%w: task %s needs -title", errUsage, sub)
		}
		e.Type, e.Title = session.EntryGoal, *title
		if sub == "add-task" {
			if *ws == "" {
				return fmt.Errorf("%w: task add-task needs -ws", errUsage)
			}
			e.Type, e.Workstream, e.Profile = session.EntryAdd, *ws, *profile
			for id := range strings.SplitSeq(*after, ",") {
				if id = strings.TrimSpace(id); id != "" {
					e.After = append(e.After, id)
				}
			}
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
			"Task %q will be added to workstream %s.\n",
			e.Title,
			e.Workstream,
		)
	case session.EntryGoal:
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
