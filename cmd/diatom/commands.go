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
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/harness"
	"github.com/dmikalova/diatom/internal/hook"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/registry"
	"github.com/dmikalova/diatom/internal/review"
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
	unlock, err := reg.LockScheduler()
	if err != nil {
		return err
	}
	defer unlock()
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

func cmdGoal(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: goal needs a subcommand", errUsage)
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "new":
		return goalNew(ctx, s, rest, stdin, stdout)
	case "approve":
		return goalApprove(ctx, s, rest, stdout)
	case "plan":
		return goalPlan(s, rest, stdout)
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
	case "done":
		return goalDone(ctx, s, rest, stdout)
	case "finish":
		return goalFinish(ctx, s, rest, stdout)
	case "activate", "park", "pin", "unpin":
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
		default:
			g.Pinned = sub == "pin"
		}
		return s.SaveGoal(g)
	}
	return fmt.Errorf("%w: unknown goal subcommand %q", errUsage, args[0])
}

// goalDone finishes a goal, but first warns about hunks nobody has decided
// on, deferred ones included (ADR 0001), and tasks not done; -force finishes
// it anyway. A goal a session is still working on is never finished. The
// goal's commits are then laid out for landing (ADR 0003).
func goalDone(ctx context.Context, s *queue.Store, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("goal done", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	force := fs.Bool("force", false, "finish the goal with hunks unreviewed or tasks not done")
	name, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(name) != 1 {
		return fmt.Errorf("%w: goal done takes a goal name", errUsage)
	}
	g, err := s.Goal(name[0])
	if err != nil {
		return err
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
				"with `diatom goal park %s` and let the session end first", t.ID, g.Name, g.Name)
		case queue.Pending, queue.Blocked:
			open++
		}
	}
	items, err := review.Load(ctx, s, g.Name)
	if err != nil {
		return err
	}
	counts := review.Counts(items)
	if left := counts[""] + counts[review.Defer]; (left > 0 || open > 0) && !*force {
		return fmt.Errorf(
			"goal %s has %d unreviewed and %d deferred hunks and %d tasks not done: review the hunks "+
				"with `diatom review`, or finish it anyway with -force",
			g.Name,
			counts[""],
			counts[review.Defer],
			open,
		)
	}
	g.State = queue.GoalDone
	if err := s.SaveGoal(g); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "goal %s is done\n", g.Name)
	res, err := layOut(ctx, s, g, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "Its commits couldn't be laid out for landing: %v\n"+
			"%s holds all of its work.\n", err, g.IntegrationBranch())
		return nil
	}
	_, _ = fmt.Fprint(stdout, finish.Describe(g, res))
	return nil
}

// goalFinish lands a done goal: it shows the goal laid out for landing, or
// with -push pushes it straight to the base branch, or with -prs opens its
// stack of pull requests. A layout the goal's branches have moved past is
// laid out again first.
func goalFinish(ctx context.Context, s *queue.Store, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("goal finish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	push := fs.Bool("push", false, "push the goal straight to its base branch")
	prs := fs.Bool("prs", false, "push the goal's branches and open a pull request for each")
	remote := fs.String("remote", "origin", "the remote to push to")
	force := fs.Bool("force", false, "push even though the gate fails")
	name, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(name) != 1 || *push && *prs {
		return fmt.Errorf("%w: goal finish takes a goal name, and -push or -prs", errUsage)
	}
	g, err := s.Goal(name[0])
	if err != nil {
		return err
	}
	if g.State != queue.GoalDone {
		return fmt.Errorf(
			"goal %s is %s: finish it with `diatom goal done %s` first",
			g.Name,
			g.State,
			g.Name,
		)
	}
	res, err := finish.Load(s.GoalDir(g.Name))
	if err != nil {
		return err
	}
	if res == nil || !finish.Current(ctx, s, g, res) {
		if res, err = layOut(ctx, s, g, stdout); err != nil {
			return err
		}
	}
	switch {
	case *push:
		if last := res.Stack[len(res.Stack)-1]; last.Gate != nil && !last.Gate.Passed && !*force {
			return fmt.Errorf("goal %s fails the gate on %s: fix it, or push anyway with -force",
				g.Name, res.Final)
		}
		if err := finish.Push(ctx, s, g, res, *remote); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "pushed %s to %s on %s\n", res.Final, g.Base, *remote)
		if local, err := (git.Repo{Dir: s.Repo()}).RevParse(
			ctx,
			g.Base,
		); err == nil &&
			local != res.Tip() {
			_, _ = fmt.Fprintf(
				stdout,
				"Your %s is behind it now: `git pull` brings it up to date.\n",
				g.Base,
			)
		}
	case *prs:
		urls, err := finish.OpenPRs(ctx, s, g, res, *remote, finish.RunGH)
		for _, u := range urls {
			_, _ = fmt.Fprintln(stdout, u)
		}
		return err
	default:
		_, _ = fmt.Fprint(stdout, finish.Describe(g, res))
	}
	return nil
}

// layOut lays a done goal out for landing, running the gate on each pull
// request.
func layOut(
	ctx context.Context,
	s *queue.Store,
	g *queue.Goal,
	stdout io.Writer,
) (*finish.Result, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(s.Repo(), paths)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(stdout, "laying goal %s out on %s, running the gate on each pull request…\n",
		g.Name, g.Base)
	return finish.Build(ctx, s, g, finish.Options{Gate: cfg.Gate})
}

// goalNew creates a goal on the current branch. It starts in planning with a
// grilling task holding the description from stdin (ADR 0010); -active skips
// grilling for a goal whose workstreams and tasks are written by hand.
func goalNew(
	ctx context.Context,
	s *queue.Store,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
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
	if !*active {
		if *ws != "" {
			return fmt.Errorf(
				"%w: a goal in planning gets its workstreams from its plan; -ws needs -active",
				errUsage,
			)
		}
		body, err := readBody(stdin)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*title) == "" {
			*title = name[0]
		}
		g, err := plan.NewGoal(
			ctx,
			s,
			name[0],
			*title,
			body,
			queue.Origin{Type: "human"},
			time.Now(),
		)
		if err != nil {
			return err
		}
		if err := registerRepo(s); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(
			stdout,
			"goal %s created from %s, in planning: grilling starts once `diatom run` "+
				"picks it up\n",
			g.Name,
			g.Base,
		)
		return nil
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
	g.State = queue.GoalActive
	if err := s.CreateGoal(g); err != nil {
		return err
	}
	if err := registerRepo(s); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "goal %s created from %s, %s\n", g.Name, base, g.State)
	return nil
}

// registerRepo makes the store's repo known to the scheduler.
func registerRepo(s *queue.Store) error {
	reg, err := registry.Default()
	if err != nil {
		return err
	}
	return reg.Add(s.Repo())
}

// goalApprove signs a goal's plan off (ADR 0010).
func goalApprove(ctx context.Context, s *queue.Store, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: goal approve takes a goal name", errUsage)
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(s.Repo(), paths)
	if err != nil {
		return err
	}
	if err := plan.Approve(ctx, s, cfg, args[0], time.Now()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "goal %s is signed off and active\n", args[0])
	return nil
}

// goalPlan prints a goal's plan.
func goalPlan(s *queue.Store, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: goal plan takes a goal name", errUsage)
	}
	p, err := plan.Load(s.GoalDir(args[0]))
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("goal %s has no plan yet", args[0])
	}
	_, _ = io.WriteString(stdout, plan.Describe(p))
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
		return taskReport(ctx, sub, rest, stdout)
	case "add-task", "new-goal", "plan":
		return planningReport(sub, rest, stdin, stdout)
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
		if spec.Kind == queue.Triage || spec.Kind == queue.Grilling {
			return hook.StopPlanning(dir, spec, stdout)
		}
		return hook.Stop(ctx, dir, spec, gate.Run, stdout)
	}
	return errors.Join(errUsage, fmt.Errorf("unknown hook %q", args[0]))
}
