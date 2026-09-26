package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/harness"
	"github.com/dmikalova/diatom/internal/hook"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/registry"
	"github.com/dmikalova/diatom/internal/runner/claude"
	"github.com/dmikalova/diatom/internal/session"
)

// cmdRun runs the scheduler. The first interrupt stops new sessions and waits
// for the running ones; a second ends them too.
func cmdRun(stop context.Context, stderr io.Writer) error {
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	reg, err := registry.Default()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	kill, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stop.Done()
		again := make(chan os.Signal, 1)
		signal.Notify(again, os.Interrupt)
		<-again
		cancel()
	}()
	log := slog.New(slog.NewTextHandler(stderr, nil))
	log.Info("diatom scheduler starting", "registry", reg.Path)
	h := &harness.Harness{
		Paths:  paths,
		Repos:  reg.List,
		Runner: claude.Runner{},
		Exe:    exe,
		Log:    log,
	}
	return h.Run(stop, kill)
}

// here opens the store of the repository the current directory is in.
func here(ctx context.Context) (*queue.Store, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := git.Root(ctx, wd)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository: %w", err)
	}
	return queue.Open(root), nil
}

func cmdGoal(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: goal needs a subcommand", errUsage)
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "new":
		return goalNew(ctx, s, rest, stdout)
	case "list":
		goals, err := s.Goals()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, g := range goals {
			pin := ""
			if g.Pinned {
				pin = "pinned"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", g.Name, g.State, pin, g.Title)
		}
		return w.Flush()
	case "activate", "park", "pin", "unpin", "done":
		if len(rest) != 1 {
			return fmt.Errorf("%w: goal %s takes a goal name", errUsage, sub)
		}
		g, err := s.Goal(rest[0])
		if err != nil {
			return err
		}
		switch sub {
		case "activate":
			g.State = queue.GoalActive
		case "park":
			g.State = queue.GoalParked
		case "done":
			g.State = queue.GoalDone
		default:
			g.Pinned = sub == "pin"
		}
		return s.SaveGoal(g)
	}
	return fmt.Errorf("%w: unknown goal subcommand %q", errUsage, args[0])
}

// goalNew creates a goal on the current branch. Until grilling lands
// (ADR 0010), -active starts it straight away with hand-written tasks.
func goalNew(ctx context.Context, s *queue.Store, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("goal new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	title := fs.String("title", "", "what the goal is for")
	ws := fs.String("ws", "", "workstreams, comma-separated, each as name or name:dep+dep")
	active := fs.Bool("active", false, "start the goal active instead of planning")
	name, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(name) != 1 {
		return fmt.Errorf("%w: goal new takes one goal name", errUsage)
	}
	repo := git.Repo{Dir: s.Repo()}
	if !repo.IsIgnored(ctx, config.DirName+"/") {
		return fmt.Errorf(
			"%s/ is not ignored in %s: add it to the global excludes file (~/.config/git/ignore)",
			config.DirName,
			s.Repo(),
		)
	}
	base, err := repo.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("the goal branches from the current branch, and there is none: %w", err)
	}
	workstreams, err := parseWorkstreams(*ws)
	if err != nil {
		return err
	}
	g := &queue.Goal{
		Name: name[0], Title: *title, State: queue.GoalPlanning, Base: base,
		Created: time.Now(), Workstreams: workstreams,
	}
	if g.Title == "" {
		g.Title = g.Name
	}
	if *active {
		g.State = queue.GoalActive
	}
	if err := s.CreateGoal(g); err != nil {
		return err
	}
	reg, err := registry.Default()
	if err != nil {
		return err
	}
	if err := reg.Add(s.Repo()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "goal %s created from %s, %s\n", g.Name, base, g.State)
	return nil
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

func parseWorkstreams(spec string) ([]queue.Workstream, error) {
	var out []queue.Workstream
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, deps, _ := strings.Cut(part, ":")
		w := queue.Workstream{Name: name}
		for d := range strings.SplitSeq(deps, "+") {
			if d != "" {
				w.DependsOn = append(w.DependsOn, d)
			}
		}
		out = append(out, w)
	}
	for _, w := range out {
		for _, d := range w.DependsOn {
			if !slices.ContainsFunc(out, func(o queue.Workstream) bool { return o.Name == d }) {
				return nil, fmt.Errorf("workstream %s depends on unknown workstream %s", w.Name, d)
			}
		}
	}
	return out, nil
}

func cmdTask(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: task needs a subcommand", errUsage)
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "add":
		return taskAdd(ctx, rest, stdin, stdout)
	case session.EntryDone, session.EntryNote, session.EntryAsk:
		return taskReport(sub, rest, stdout)
	}
	return fmt.Errorf("%w: unknown task subcommand %q", errUsage, args[0])
}

func taskAdd(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("task add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	goal := fs.String("goal", "", "the goal")
	ws := fs.String("ws", "", "the workstream")
	kind := fs.String("kind", string(queue.Planned), "the task kind")
	profile := fs.String("profile", "", "the profile, instead of the kind's default")
	after := fs.String("after", "", "ids of tasks that must be done first, comma-separated")
	priority := fs.Int("priority", 0, "order among tasks of the same kind; lower runs first")
	title, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *goal == "" || *ws == "" || len(title) == 0 {
		return fmt.Errorf("%w: task add needs -goal, -ws and a title", errUsage)
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	g, err := s.Goal(*goal)
	if err != nil {
		return err
	}
	if _, ok := g.Workstream(*ws); !ok {
		return fmt.Errorf("goal %s has no workstream %s", g.Name, *ws)
	}
	body, err := readBody(stdin)
	if err != nil {
		return err
	}
	t := &queue.Task{
		Title: strings.Join(
			title,
			" ",
		),
		Kind:       queue.Kind(*kind),
		Profile:    *profile,
		Workstream: *ws,
		Priority:   *priority,
		Origin:     queue.Origin{Type: "human"},
		Created:    time.Now(),
		Body:       body,
	}
	for id := range strings.SplitSeq(*after, ",") {
		if id = strings.TrimSpace(id); id != "" {
			t.DependsOn = append(t.DependsOn, id)
		}
	}
	if err := s.AddTask(g.Name, t); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, t.ID)
	return nil
}

// readBody reads a task body from stdin when it is piped, not a terminal.
func readBody(stdin io.Reader) (string, error) {
	if f, ok := stdin.(*os.File); ok {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return "", nil
		}
	}
	b, err := io.ReadAll(stdin)
	return string(b), err
}

// taskReport is the agent's task tool.
func taskReport(typ string, args []string, stdout io.Writer) error {
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

func cmdQuestions(ctx context.Context, stdout io.Writer) error {
	s, err := here(ctx)
	if err != nil {
		return err
	}
	goals, err := s.Goals()
	if err != nil {
		return err
	}
	for _, g := range goals {
		qs, err := s.Questions(g.Name, queue.QuestionOpen)
		if err != nil {
			return err
		}
		for _, q := range qs {
			if q.Answer != "" {
				continue
			}
			_, _ = fmt.Fprintf(
				stdout,
				"%s %s (task %s)\n%s\n",
				g.Name,
				q.ID,
				q.Task,
				indent(q.Text),
			)
		}
	}
	return nil
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

func cmdAnswer(ctx context.Context, args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("%w: answer takes a goal, a question id and the answer", errUsage)
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	return s.Answer(args[0], args[1], strings.Join(args[2:], " "), time.Now())
}

func cmdStatus(ctx context.Context, stdout io.Writer) error {
	reg, err := registry.Default()
	if err != nil {
		return err
	}
	repos, err := reg.List()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, repo := range repos {
		s := queue.Open(repo)
		goals, err := s.Goals()
		if err != nil {
			return err
		}
		for _, g := range goals {
			if g.State == queue.GoalDone {
				continue
			}
			tasks, err := s.Tasks(g.Name)
			if err != nil {
				return err
			}
			counts := map[queue.State]int{}
			var active []string
			for _, t := range tasks {
				counts[t.State]++
				if t.State == queue.Active {
					active = append(active, t.Workstream+"/"+t.ID)
				}
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\tpending %d\tactive %d\tblocked %d\tdone %d\t%s\n",
				filepath.Base(repo), g.Name, g.State, counts[queue.Pending], counts[queue.Active],
				counts[queue.Blocked], counts[queue.Done], strings.Join(active, " "))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return ctx.Err()
}

func cmdHook(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: hook takes pre-tool-use or stop", errUsage)
	}
	switch args[0] {
	case "pre-tool-use":
		return hook.PreToolUse(stdin, stdout)
	case "stop":
		// The event itself carries nothing the gate needs.
		_, _ = io.Copy(io.Discard, stdin)
		dir, spec, err := session.FromEnv()
		if err != nil {
			return err
		}
		return hook.Stop(ctx, dir, spec, gate.Run, stdout)
	}
	return errors.Join(errUsage, fmt.Errorf("unknown hook %q", args[0]))
}
