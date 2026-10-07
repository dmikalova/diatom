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
	case "help", "-h", "--help":
		_, _ = fmt.Fprintln(stdout, usage)
		return nil
	case session.EntryDone, session.EntryNote, session.EntryAsk, session.EntryManual:
		return taskReport(ctx, sub, rest, stdout)
	case session.EntryConnect:
		return taskConnect(rest, stdout)
	case session.EntryCI:
		return taskCI(rest, stdout)
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

// taskConnect records that a task needs a connector the session doesn't
// have. The harness runs the batch again with it attached (ADR 0013).
func taskConnect(args []string, stdout io.Writer) error {
	if len(args) < 3 {
		return fmt.Errorf(
			"%w: task connect takes a task id, a connector's name and why it is needed",
			errUsage,
		)
	}
	dir, spec, err := session.FromEnv()
	if err != nil {
		return err
	}
	if !slices.Contains(spec.Connectors, args[1]) {
		return fmt.Errorf("no connector %q in this repo; it has %s",
			args[1], strings.Join(spec.Connectors, ", "))
	}
	e := session.Entry{
		Type:   session.EntryConnect,
		Task:   args[0],
		Server: args[1],
		Text:   strings.Join(args[2:], " "),
	}
	if err := session.Append(dir, spec, e); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(
		stdout,
		"Task %s will get the %s connector. This session can't, so stop working on that task and "+
			"end the session once the others are done; diatom runs it again with the connector.\n",
		e.Task,
		e.Server,
	)
	return nil
}

func taskCI(args []string, stdout io.Writer) error {
	labels, args := cutLabels(args)
	if len(args) < 2 {
		return fmt.Errorf("%w: task ci takes a task id, why the checks are needed, and any "+
			"number of --label <name>", errUsage)
	}
	dir, spec, err := session.FromEnv()
	if err != nil {
		return err
	}
	e := session.Entry{
		Type:   session.EntryCI,
		Task:   args[0],
		Text:   strings.Join(args[1:], " "),
		Labels: labels,
	}
	if err := session.Append(dir, spec, e); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(
		stdout,
		"Diatom will push the goal's branch, open its pull request if it has none, and put the "+
			"checks on task %s. They take longer than this session may wait, so stop working on "+
			"that task and end the session once the others are done; diatom runs it again with "+
			"the results.\n",
		e.Task,
	)
	if len(labels) > 0 {
		_, _ = fmt.Fprintf(stdout, "It puts %s on the pull request for this run, and takes them "+
			"off again with the verdict.\n", strings.Join(labels, ", "))
	}
	return nil
}

// cutLabels takes the --label <name> and --label=<name> options out of args.
func cutLabels(args []string) (labels, rest []string) {
	for i := 0; i < len(args); i++ {
		name, value, attached := strings.Cut(args[i], "=")
		if name != "--label" && name != "-l" {
			rest = append(rest, args[i])
			continue
		}
		if !attached && i+1 < len(args) {
			i++
			value = args[i]
		}
		if value != "" {
			labels = append(labels, value)
		}
	}
	return labels, rest
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
		released, err := releaseRest(dir, spec)
		if err != nil {
			return err
		}
		if len(released) > 0 {
			_, _ = fmt.Fprintf(
				stdout,
				"This session's context is large now, so its other tasks (%s) go "+
					"to fresh sessions, which cost less per step. Don't start them: end the session now.\n",
				strings.Join(released, ", "),
			)
		}
	case session.EntryAsk:
		_, _ = fmt.Fprintf(
			stdout,
			"Your question is queued for the human and task %s is parked. Move on to the other tasks.\n",
			e.Task,
		)
	case session.EntryManual:
		_, _ = fmt.Fprintf(
			stdout,
			"Your steps are queued for the human and task %s is parked until they say they are done. "+
				"Move on to the other tasks.\n",
			e.Task,
		)
	default:
		_, _ = fmt.Fprintf(stdout, "Note added to task %s.\n", e.Task)
	}
	return nil
}

// releaseRest hands the session's tasks it hasn't reported on back to the
// queue once its context passes the spec's ChainContext, returning them: a
// fresh session reads its base once, where every step of a long one reads
// the whole of it again (ADR 0004).
func releaseRest(dir string, spec session.Spec) ([]string, error) {
	if spec.ChainContext <= 0 || session.Context(dir) < spec.ChainContext {
		return nil, nil
	}
	report, err := session.ReadReport(dir)
	if err != nil {
		return nil, err
	}
	reported := map[string]bool{}
	for _, e := range slices.Concat(report.Finished, report.Questions) {
		reported[e.Task] = true
	}
	var released []string
	for _, id := range spec.Tasks {
		if reported[id] || report.Released[id] {
			continue
		}
		if err := session.Append(
			dir,
			spec,
			session.Entry{Type: session.EntryRelease, Task: id},
		); err != nil {
			return released, err
		}
		released = append(released, id)
	}
	return released, nil
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
		return hook.Stop(
			ctx,
			dir,
			spec,
			gate.Serial(gate.Within(spec.GateTimeout, gate.Run)),
			stdout,
		)
	}
	return errors.Join(errUsage, fmt.Errorf("unknown hook %q", args[0]))
}
