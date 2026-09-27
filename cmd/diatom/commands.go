package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/hook"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/roster"
	"github.com/dmikalova/diatom/internal/session"
)

// here opens the store of the repository the current directory is in,
// anywhere in it, a goal's worktree included. diatom works in one repo at a
// time (ADR 0007), and only in a repo that ignores its state.
func here(ctx context.Context) (*queue.Store, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := git.Root(ctx, wd)
	if err != nil {
		return nil, errors.New("not in a git repository: diatom works in the repo it runs in")
	}
	if !(git.Repo{Dir: root}).IsIgnored(ctx, config.DirName+"/") {
		return nil, fmt.Errorf(
			"%s/ is not ignored in %s: add it to the global excludes file (~/.config/git/ignore)",
			config.DirName,
			root,
		)
	}
	return queue.Open(root), nil
}

// parseInterspersed parses flags that may come after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%w: %w", errUsage, err)
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// splitList splits a comma-separated list, dropping empty items.
func splitList(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func cmdTask(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: task needs a subcommand", errUsage)
	}
	switch sub, rest := args[0], args[1:]; sub {
	case session.EntryDone, session.EntryNote, session.EntryAsk:
		return taskReport(ctx, sub, rest, stdout)
	case "add-task", "after", "feedback", "new-goal", "plan":
		return planningReport(sub, rest, stdin, stdout)
	case "goals":
		return taskGoals(ctx, rest, stdout)
	}
	return fmt.Errorf("%w: unknown task subcommand %q", errUsage, args[0])
}

// taskGoals is how an agent reads the repo's goals: all of them in brief, or
// one in full.
func taskGoals(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 1 {
		return fmt.Errorf("%w: task goals takes at most a goal's name", errUsage)
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	briefs, err := roster.Briefs(s)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return roster.Write(stdout, briefs, "")
	}
	if !slices.ContainsFunc(briefs, func(b roster.Brief) bool { return b.Name == args[0] }) {
		names := make([]string, 0, len(briefs))
		for _, b := range briefs {
			names = append(names, b.Name)
		}
		return fmt.Errorf("no goal %q; the goals are %s", args[0], strings.Join(names, ", "))
	}
	return roster.Detail(stdout, s, args[0])
}

// taskReport is the agent's task tool. In a revision session, marking a task
// done also snapshots the worktree, so the harness can commit each revision
// as its own fixup.
func taskReport(ctx context.Context, typ string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: task %s takes a task id", errUsage, typ)
	}
	if typ != session.EntryDone && len(args) < 2 {
		return fmt.Errorf("%w: task %s takes a task id and text", errUsage, typ)
	}
	dir, spec, err := session.FromEnv()
	if err != nil {
		return err
	}
	e := session.Entry{Type: typ, Task: args[0], Text: strings.Join(args[1:], " ")}
	if typ == session.EntryDone && spec.Kind == queue.Revision {
		if e.Tree, err = (git.Repo{Dir: spec.Worktree}).Fingerprint(ctx); err != nil {
			return err
		}
	}
	if err := session.Append(dir, spec, e); err != nil {
		return err
	}
	switch typ {
	case session.EntryDone:
		_, _ = fmt.Fprintf(stdout, "Task %s is marked done.\n", e.Task)
	case session.EntryAsk:
		_, _ = fmt.Fprintf(
			stdout,
			"Your question is queued for the human and task %s is parked. Move on to the other tasks.\n",
			e.Task,
		)
	default:
		_, _ = fmt.Fprintf(stdout, "Note added to task %s.\n", e.Task)
	}
	return nil
}

// hookScope is the part of diatom's state the session's file tools may reach:
// its worktree, its tasks and its goal's ADR drafts. Outside a session
// nothing is held back.
func hookScope() hook.Scope {
	_, spec, err := session.FromEnv()
	if err != nil || spec.Repo == "" {
		return hook.Scope{}
	}
	s := queue.Open(spec.Repo)
	goal := s.GoalDir(spec.Goal)
	return hook.Scope{State: s.Root, Allowed: []string{
		spec.Worktree,
		filepath.Join(goal, "tasks", string(queue.Active)),
		plan.DraftsDir(goal),
	}}
}

func cmdHook(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: hook takes pre-tool-use or stop", errUsage)
	}
	switch args[0] {
	case "pre-tool-use":
		return hook.PreToolUse(stdin, stdout, hookScope())
	case "stop":
		// The event itself carries nothing the gate needs.
		_, _ = io.Copy(io.Discard, stdin)
		dir, spec, err := session.FromEnv()
		if err != nil {
			return err
		}
		if spec.Kind == queue.Triage || spec.Kind == queue.Grilling {
			return hook.StopPlanning(dir, spec, stdout)
		}
		return hook.Stop(ctx, dir, spec, gate.Within(spec.GateTimeout, gate.Run), stdout)
	}
	return errors.Join(errUsage, fmt.Errorf("unknown hook %q", args[0]))
}
