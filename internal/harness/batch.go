package harness

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/commitmsg"
	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/roster"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

// maxIncomplete is how many sessions may end without finishing a task before
// it is retried with more effort, the same as a gate that keeps failing.
const maxIncomplete = 2

// RunBatch runs one batch to its end:
//
//  1. Merge the integration branch into the workstream (ADR 0003).
//  2. Run the agent session, whose Stop hook runs the gate (ADR 0005).
//  3. Run the gate unless the hook already passed it on the same files.
//  4. Commit, with a message from the commit-message profile.
//  5. Merge the workstream into the integration branch.
//  6. Move each task to done, blocked on its question, or back to pending.
//
// Cancelling ctx suspends the batch: the agent stops within seconds and its
// tasks stay active, so the next scheduler resumes the session where it
// stopped. The gate stops too and runs again; a commit or merge under way
// always finishes first.
func (h *Harness) RunBatch(ctx context.Context, repo Repo, b schedule.Batch) error {
	err := h.runBatch(ctx, repo, b)
	if errors.Is(err, errSuspended) {
		h.log().Info("session suspended; it resumes when diatom starts again",
			"goal", b.Goal, "workstream", b.Workstream)
		return nil
	}
	return err
}

// errSuspended is a batch stopped midway, to be resumed.
var errSuspended = errors.New("suspended")

func (h *Harness) runBatch(ctx context.Context, repo Repo, b schedule.Batch) error {
	s := repo.Store
	g, err := s.Goal(b.Goal)
	if err != nil {
		return err
	}
	if planningKind(b.Kind) {
		return h.runPlanning(ctx, repo, g, b)
	}
	if strings.TrimSpace(repo.Config.Gate) == "" {
		// Without a gate nothing can be checked or committed, so a session
		// would only be thrown away.
		return h.askAll(s, g.Name, b.Tasks, noGate)
	}
	main := git.Repo{Dir: s.Repo()}
	wt := git.Repo{Dir: s.WorktreeDir(g.Name, b.Workstream)}
	unlock := h.lockRepo(s.Repo())
	err = main.CreateBranch(ctx, g.IntegrationBranch(), h.goalStart(ctx, main, g))
	if err == nil {
		err = main.EnsureWorktree(
			ctx,
			wt.Dir,
			g.WorkstreamBranch(b.Workstream),
			g.IntegrationBranch(),
		)
	}
	unlock()
	if err != nil {
		return err
	}
	if run, err := h.prepare(ctx, repo, g, b, wt); err != nil || !run {
		return err
	}

	for _, t := range b.Tasks {
		if err := s.Move(g.Name, t, queue.Active); err != nil {
			return err
		}
	}
	dir, spec, err := h.newSession(ctx, repo, g, b, wt)
	if err != nil {
		return h.requeue(s, g.Name, b.Tasks, nil, err)
	}
	return h.runAgent(ctx, repo, g, b, wt, dir, spec, "", "")
}

// runAgent runs a session's agent, carrying on the agent session resume when
// it is set, with prompt as the next message, or the session's prompt.md
// when prompt is empty, then settles the session.
func (h *Harness) runAgent(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
	dir string,
	spec session.Spec,
	resume, prompt string,
) error {
	res, runErr := h.runSession(ctx, repo, g, b, wt, dir, spec, resume, prompt)
	if ctx.Err() != nil {
		// What the agent spent before the stop is spent, though the
		// session carries on.
		if res.Usage.CostUSD > 0 {
			run := session.Run{Ended: h.now(), CostUSD: res.Usage.CostUSD}
			if err := session.UpdateState(dir, func(st *session.State) {
				st.Earlier = append(st.Earlier, run)
			}); err != nil {
				h.log().
					Warn("recording the stopped agent's cost failed", "session", spec.ID, "err", err)
			}
		}
		return errSuspended
	}
	if err := session.WriteResult(
		dir,
		sessionResult{Result: res, Error: errString(runErr)},
	); err != nil {
		h.log().Warn("writing the session result failed", "session", spec.ID, "err", err)
	}
	return h.settleSession(ctx, repo, g, b, wt, dir, res, runErr)
}

// settleSession applies what a session's agent did and moves its tasks on,
// then marks the session settled.
func (h *Harness) settleSession(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
	dir string,
	res runner.Result,
	runErr error,
) error {
	var err error
	if planningKind(b.Kind) {
		// Nothing slow is left, so a stop doesn't interrupt it.
		err = h.finishPlanning(context.WithoutCancel(ctx), repo, g, b, dir, res, runErr)
	} else {
		err = h.finish(ctx, repo, g, b, wt, dir, res, runErr)
	}
	if errors.Is(err, errSuspended) {
		return err
	}
	return errors.Join(err, session.UpdateState(dir, func(st *session.State) {
		st.Settled, st.Error = true, errString(err)
	}))
}

type sessionResult struct {
	runner.Result
	Error string `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// prepare merges the integration branch into the workstream before the batch
// and reports whether the batch can run, then the base branch too for a task
// catching the goal up with it. A merge that conflicts or fails the gate is
// left in progress for a conflict or gate-repair task, which runs first
// because it has the higher priority (ADR 0004).
func (h *Harness) prepare(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
) (bool, error) {
	fixing := b.Kind == queue.Conflict || b.Kind == queue.GateRepair
	if wt.MergeInProgress(ctx) {
		if !fixing {
			h.log().
				Info("waiting for the merge in progress to be fixed", "goal", g.Name, "workstream", b.Workstream)
		}
		return fixing, nil
	}
	refs := []string{g.IntegrationBranch()}
	for _, t := range b.Tasks {
		if t.Merge != "" {
			refs = append(refs, t.Merge)
		}
	}
	for _, ref := range refs {
		if merged, run, err := h.mergeIn(ctx, repo, g, b, wt, ref, fixing); err != nil || !merged {
			return run, err
		}
	}
	if b.Kind == queue.Conflict {
		// The merges went through cleanly, so nothing is left to resolve.
		for _, t := range b.Tasks {
			t.Body = appendSection(
				t.Body,
				"Resolved",
				"Everything merged into the workstream without conflicts.",
			)
			if err := repo.Store.Move(g.Name, t, queue.Done); err != nil {
				return false, err
			}
		}
		return false, h.integrate(ctx, repo, g, b.Workstream)
	}
	return true, nil
}

// mergeIn merges ref into the workstream. It reports whether the merge is
// made, and when it isn't, whether the batch runs to fix it: a conflict, or a
// merge failing the gate, is left in progress for the batch when it is the
// fix, and queues one otherwise.
func (h *Harness) mergeIn(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
	ref string,
	fixing bool,
) (merged, run bool, err error) {
	res, err := wt.MergeNoCommit(ctx, ref)
	if err != nil {
		return false, false, err
	}
	switch res {
	case git.Conflicted:
		if fixing {
			return false, true, nil
		}
		return false, false, h.addFix(
			repo.Store,
			g.Name,
			b.Workstream,
			queue.Conflict,
			"Merging "+ref+" into this workstream conflicts. The merge is in progress in the "+
				"worktree: resolve every conflicted file, keeping the intent of both sides.",
		)
	case git.Merged:
		r, err := h.runGate(ctx, wt.Dir, repo.Config, repo.Config.Gate)
		if ctx.Err() != nil {
			// Stopped: undo the merge, which is made again next time.
			return false, false, errors.Join(
				errSuspended,
				wt.AbortMerge(context.WithoutCancel(ctx)),
			)
		}
		if err != nil {
			return false, false, err
		}
		if !r.Passed {
			if fixing {
				return false, true, nil
			}
			return false, false, h.addFix(
				repo.Store,
				g.Name,
				b.Workstream,
				queue.GateRepair,
				"Merging "+ref+" into this workstream fails the gate. The merge is in progress in "+
					"the worktree: make the gate pass.\n\n```text\n"+r.Output+"\n```",
			)
		}
		if _, err := wt.CommitMerge(ctx); err != nil {
			return false, false, err
		}
	}
	return true, false, nil
}

// addFix queues a conflict or gate-repair task for a workstream, or adds the
// news to the one already waiting.
func (h *Harness) addFix(s *queue.Store, goal, ws string, kind queue.Kind, body string) error {
	tasks, err := s.Tasks(goal)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Kind == kind && t.Workstream == ws && t.State != queue.Done {
			t.Body = appendSection(t.Body, "Update "+h.now().Format(time.DateTime), body)
			return s.SaveTask(goal, t)
		}
	}
	title := "Resolve the merge conflict with the integration branch"
	if kind == queue.GateRepair {
		title = "Make the gate pass"
	}
	return s.AddTask(goal, &queue.Task{
		Title: title, Kind: kind, Workstream: ws, Created: h.now(),
		Origin: queue.Origin{Type: "harness"}, Body: body,
	})
}

// newSession writes the session directory and its prompt.
func (h *Harness) newSession(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
) (string, session.Spec, error) {
	s, cfg := repo.Store, repo.Config
	id, dir, err := h.sessionDir(s.SessionsDir(g.Name), b.Workstream)
	if err != nil {
		return "", session.Spec{}, err
	}
	spec := session.Spec{
		ID: id, Repo: s.Repo(), Goal: g.Name, Workstream: b.Workstream, Worktree: wt.Dir,
		Kind: b.Kind, Profile: b.Profile, Effort: b.Effort,
		Gate: cfg.SessionGate(), GateAttempts: cfg.GateAttempts, GateTimeout: cfg.GateTimeout,
		ChainContext: cfg.ChainContext,
		Connectors:   catalog(cfg),
		Attached:     attached(cfg, b.Tasks, planningKind(b.Kind)),
		Tickets:      cfg.Tickets,
		Feedback:     sentBack(b.Tasks),
	}
	if p, err := cfg.Profile(b.Profile); err == nil {
		spec.Model, spec.Level = p.Model, cmp.Or(b.Effort, p.Effort)
	}
	for _, t := range b.Tasks {
		spec.Tasks = append(spec.Tasks, t.ID)
	}
	if err := session.Create(dir, spec); err != nil {
		return "", spec, err
	}
	// The task tool is `diatom task`, found on the agent's PATH.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return "", spec, err
	}
	if err := os.Symlink(h.Exe, filepath.Join(bin, "diatom")); err != nil {
		return "", spec, err
	}
	guides, err := nestedGuides(ctx, wt)
	if err != nil {
		return "", spec, err
	}
	goals, err := roster.Briefs(s)
	if err != nil {
		return "", spec, err
	}
	in := PromptInput{
		Goal: g, Batch: b, Gate: cfg.Gate, Fix: cfg.Fix, Timeout: cfg.CommandTimeout,
		TaskDir:    filepath.Join(s.GoalDir(g.Name), "tasks", string(queue.Active)),
		Merging:    wt.MergeInProgress(ctx),
		Guides:     guides,
		Goals:      goals,
		Connectors: cfg.Connectors(),
		Attached:   spec.Attached,
		Tickets:    cfg.Tickets,
		Feedback:   spec.Feedback,
	}
	var prompt string
	if planningKind(b.Kind) {
		if prompt, err = h.planningPrompt(repo, g, in); err != nil {
			return "", spec, err
		}
	} else {
		prompt = Prompt(in)
	}
	return dir, spec, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0o644)
}

// sessionDir makes a new session directory named for the time and the
// workstream.
func (h *Harness) sessionDir(root, ws string) (id, dir string, err error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	if ws == "" {
		ws = "planning"
	}
	base := h.now().UTC().Format("20060102T150405Z") + "-" + ws
	for n := 0; ; n++ {
		id = base
		if n > 0 {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		dir = filepath.Join(root, id)
		err := os.Mkdir(dir, 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return id, dir, err
	}
}

// cacheTTL is how long a session's prompt cache is kept (ADR 0011): an
// hour for triage and grilling, whose next round carries the session on
// once the human answers, and otherwise five minutes, which costs less to
// write and outlasts the seconds between a session's steps.
func cacheTTL(k queue.Kind) string {
	if planningKind(k) {
		return "1h"
	}
	return "5m"
}

// resumeNote is the message that resumes a session the scheduler stopped.
const resumeNote = `diatom stopped this session midway and has now restarted it: carry on where you
left off. Your files are exactly as you left them; nothing was committed, reverted or stashed. A
command that was running when the session stopped was killed, so run it again if you still need
its result. Report each task with ` + "`diatom task`" + ` as before.`

// runSession runs the agent, logging its events to events.jsonl. With resume
// set, it carries on that earlier agent session instead of starting one. The
// agent is sent prompt, or the session's prompt.md when prompt is empty.
func (h *Harness) runSession(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
	dir string,
	spec session.Spec,
	resume, prompt string,
) (runner.Result, error) {
	profile, err := repo.Config.Profile(b.Profile)
	if err != nil {
		return runner.Result{}, err
	}
	if b.Effort != "" {
		profile.Effort = b.Effort
	}
	instructions, err := h.instructions(repo.Config, repo.Store.Repo(), wt)
	if err != nil {
		return runner.Result{}, err
	}
	var skills []string
	for _, sk := range slices.Concat(repo.Config.Skills, profile.Skills) {
		if dir := h.Paths.SkillDir(sk); !slices.Contains(skills, dir) {
			skills = append(skills, dir)
		}
	}
	if len(skills) > 0 && !slices.Contains(profile.Tools, "Skill") {
		// A skill is only of use with the tool that loads it.
		profile.Tools = append(slices.Clone(profile.Tools), "Skill")
	}
	if prompt == "" {
		data, err := os.ReadFile(filepath.Join(dir, "prompt.md"))
		if err != nil {
			return runner.Result{}, err
		}
		prompt = string(data)
	}
	servers, err := repo.Config.MCPConfig(spec.Attached)
	if err != nil {
		return runner.Result{}, err
	}
	events, err := os.OpenFile(
		filepath.Join(dir, "events.jsonl"),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return runner.Result{}, err
	}
	defer func() { _ = events.Close() }()
	var mu sync.Mutex
	enc := json.NewEncoder(events)
	onEvent := func(e runner.Event) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(struct {
			Time time.Time `json:"time"`
			runner.Event
		}{h.now(), e})
	}
	exe := shellQuote(h.Exe)
	hooks := runner.Hooks{PreToolUse: exe + " hook pre-tool-use", Stop: exe + " hook stop"}
	addDirs := []string{filepath.Join(repo.Store.GoalDir(g.Name), "tasks", string(queue.Active))}
	if planningKind(b.Kind) {
		// A planning session changes no code, so its Stop hook runs no gate;
		// it only checks that every task was reported. Grilling may draft
		// ADRs beside the goal.
		addDirs = append(addDirs, plan.DraftsDir(repo.Store.GoalDir(g.Name)))
	}
	h.log().Info("session starting", "session", spec.ID, "goal", g.Name, "workstream", b.Workstream,
		"kind", b.Kind, "profile", b.Profile, "tasks", spec.Tasks, "connectors", spec.Attached,
		"resuming", resume != "")
	res, err := h.Runner.Run(ctx, runner.Spec{
		Dir:     wt.Dir,
		AddDirs: addDirs,
		Prompt:  prompt,
		Profile: profile,
		Env: []string{
			session.EnvVar + "=" + dir,
			"PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		},
		Hooks:          hooks,
		Instructions:   instructions,
		Skills:         skills,
		MCPServers:     servers,
		Resume:         resume,
		CacheTTL:       cacheTTL(b.Kind),
		CommandTimeout: repo.Config.CommandTimeout,
		Started: func(id string) {
			if err := session.UpdateState(
				dir,
				func(st *session.State) { st.AgentSession = id },
			); err != nil {
				h.log().Warn("recording the agent's session failed", "session", spec.ID, "err", err)
			}
		},
	}, onEvent)
	h.log().Info("session ended", "session", spec.ID, "outcome", res.Outcome, "turns", res.Turns,
		"costUSD", res.Usage.CostUSD, "err", err)
	return res, err
}

// instructions gathers what is appended to the agent's system prompt: the
// configured instruction files, then the AGENTS.md of every directory from
// the home directory down to the repo's, the repo's own read from the
// worktree, each file once. Claude Code reads none of them on its own, and
// the session loads no other user context (ADR 0006). The AGENTS.md files
// below the repo's root are left to the prompt, which names them for the
// agent to read before working in their directory.
func (h *Harness) instructions(cfg *config.Config, root string, wt git.Repo) (string, error) {
	paths := make([]string, 0, len(cfg.Instructions)+4)
	for _, p := range cfg.Instructions {
		paths = append(paths, h.Paths.Expand(p))
	}
	for _, dir := range above(root, h.Paths.Home) {
		paths = append(paths, filepath.Join(dir, "AGENTS.md"))
	}
	paths = append(paths, filepath.Join(wt.Dir, "AGENTS.md"))
	var parts []string
	seen := map[string]bool{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		file, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", err
		}
		if seen[file] {
			continue
		}
		seen[file] = true
		parts = append(
			parts,
			fmt.Sprintf("# Instructions from %s\n\n%s", p, string(bytes.TrimSpace(b))),
		)
	}
	return strings.Join(parts, "\n\n"), nil
}

// above lists the directories above root, furthest first, up to home, or up
// to the filesystem's root with home first when root isn't under home.
func above(root, home string) []string {
	var dirs []string
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == home || filepath.Dir(dir) == dir {
			break
		}
	}
	if home != "" && dirs[len(dirs)-1] != home {
		dirs = append(dirs, home)
	}
	slices.Reverse(dirs)
	return dirs
}

// appendSummaries adds to each task done the summary the agent gave with
// it, what the human reads about the task in place of its replies.
func appendSummaries(s *queue.Store, goal string, report session.Report) error {
	for _, e := range report.Finished {
		if strings.TrimSpace(e.Text) == "" {
			continue
		}
		if err := s.AppendNote(goal, e.Task, "Summary", e.Text); err != nil {
			return err
		}
	}
	return nil
}

// nestedGuides lists the AGENTS.md files below the worktree's root, which
// the prompt names for the agent to read before working in their directory.
func nestedGuides(ctx context.Context, wt git.Repo) ([]string, error) {
	out, err := wt.Run(ctx, "ls-files", "--", "*AGENTS.md")
	if err != nil {
		return nil, err
	}
	var guides []string
	for f := range strings.SplitSeq(out, "\n") {
		if f != "" && f != "AGENTS.md" && filepath.Base(f) == "AGENTS.md" {
			guides = append(guides, f)
		}
	}
	return guides, nil
}

// finish settles a batch after its session: gate, commit, integrate, and move
// each task on.
func (h *Harness) finish(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
	dir string,
	res runner.Result,
	runErr error,
) error {
	s := repo.Store
	// The gate comes first, before anything the session reported is
	// applied: a stop during the gate leaves nothing half done, and the
	// session settles from the start when it resumes.
	var passed bool
	var output string
	var gateErr error
	if runErr == nil {
		st, err := session.LoadGate(dir)
		if err != nil {
			return err
		}
		passed, output, gateErr = h.check(ctx, repo, wt, dir, st.Passed)
		if ctx.Err() != nil {
			return errSuspended
		}
	}
	// From here on only git runs, which a stop never interrupts.
	ctx = context.WithoutCancel(ctx)

	report, err := session.ReadReport(dir)
	if err != nil {
		return err
	}
	for _, n := range report.Notes {
		if err := s.AppendNote(g.Name, n.Task, "Note", n.Text); err != nil {
			return err
		}
	}
	if err := appendSummaries(s, g.Name, report); err != nil {
		return err
	}
	if err := h.applyConnects(repo, g.Name, report); err != nil {
		return err
	}
	tasks, err := h.reload(s, g.Name, b.Tasks)
	if err != nil {
		return err
	}
	asked := map[string]bool{}
	for _, q := range report.Questions {
		if err := s.AddQuestion(g.Name, &queue.Question{
			Task: q.Task, Text: q.Text, Manual: q.Type == session.EntryManual, Created: h.now(),
		}); err != nil {
			return err
		}
		asked[q.Task] = true
	}
	if runErr == nil {
		// A goal the human asked for, such as in answering a question, is
		// started whether or not this work passes: it is work of its own.
		ask := func(task, text string) error {
			asked[task] = true
			return s.AddQuestion(g.Name, &queue.Question{Task: task, Text: text, Created: h.now()})
		}
		if err := h.startGoals(ctx, repo, report.Goals, nil,
			queue.Origin{Type: "goal", Ref: g.Name}, ask); err != nil {
			return err
		}
	}
	if err := errors.Join(runErr, gateErr); err != nil {
		h.ciDropped(repo, g, report, "the session ended with an error, so nothing was committed")
		return h.requeue(s, g.Name, tasks, asked, err)
	}
	if !passed {
		h.ciDropped(repo, g, report, "the gate failed, so the work was stashed, not committed")
		id := filepath.Base(dir)
		return h.failed(ctx, repo, g, wt, tasks, asked, id, ended(id, res), output)
	}

	h.settling(dir, "Committing the work")
	shas, err := h.commit(ctx, repo, wt, dir, b.Kind, g.Base, tasks, report)
	if err != nil {
		return h.requeue(s, g.Name, tasks, asked, err)
	}
	if err := h.settleTasks(repo, g.Name, dir, res, tasks, shas, report, asked); err != nil {
		return err
	}
	h.settling(dir, "Merging it into the goal's integration branch")
	if err := h.integrate(ctx, repo, g, b.Workstream); err != nil {
		return err
	}
	// The checks run on the goal's branch, so the work has to be on it first.
	return h.applyCI(ctx, repo, g, report)
}

// settleTasks moves each task of a session whose work is committed on: its
// commit and usage recorded, done, blocked on a question, handed on
// unstarted, or back to the queue unfinished.
func (h *Harness) settleTasks(
	repo Repo,
	goal, dir string,
	res runner.Result,
	tasks []*queue.Task,
	shas map[string]string,
	report session.Report,
	asked map[string]bool,
) error {
	share := usageShare(filepath.Base(dir), res.Usage, len(tasks))
	waiting := map[string]bool{}
	for _, e := range report.CI {
		waiting[e.Task] = true
	}
	for _, t := range tasks {
		if sha := shas[t.ID]; sha != "" {
			t.Commits = append(t.Commits, sha)
		}
		t.Usage = append(t.Usage, share)
		if report.Released[t.ID] && !report.Done[t.ID] && !asked[t.ID] {
			// Handed on unstarted: no attempt of its was spent.
			if err := repo.Store.Move(goal, t, queue.Pending); err != nil {
				return err
			}
			continue
		}
		if err := h.settle(repo, goal, t, report.Done[t.ID], asked[t.ID], waiting[t.ID],
			ended(filepath.Base(dir), res)); err != nil {
			return err
		}
	}
	return nil
}

// settling logs a step diatom takes after the agent, so the session shows
// what it is waiting on.
func (h *Harness) settling(dir, what string) {
	if err := session.AppendEvent(dir, session.Settling(h.now(), what)); err != nil {
		h.log().Warn("logging a session step failed", "dir", dir, "err", err)
	}
}

// reload rereads the batch's tasks, which the task tool's notes may have
// changed on disk.
func (h *Harness) reload(s *queue.Store, goal string, batch []*queue.Task) ([]*queue.Task, error) {
	tasks := make([]*queue.Task, 0, len(batch))
	for _, t := range batch {
		fresh, err := s.Task(goal, t.ID)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, fresh)
	}
	return tasks, nil
}

// check reports whether the worktree passes the gate, skipping the run when
// nothing changed or the Stop hook already passed these exact files.
func (h *Harness) check(
	ctx context.Context,
	repo Repo,
	wt git.Repo,
	dir, hookPassed string,
) (bool, string, error) {
	dirty, err := wt.Dirty(ctx)
	if err != nil {
		return false, "", err
	}
	if !dirty && !wt.MergeInProgress(ctx) {
		return true, "", nil
	}
	markers, err := wt.ConflictMarkers(ctx)
	if err != nil {
		return false, "", err
	}
	if len(markers) > 0 {
		return false, "Conflict markers remain:\n" + strings.Join(markers, "\n"), nil
	}
	if fp, err := wt.Fingerprint(ctx); err != nil || fp == hookPassed {
		return err == nil, "", err
	}
	command := repo.Config.SessionGate()
	h.settling(dir, "Running the gate `"+command+"`")
	start := h.now()
	r, err := h.runGate(ctx, wt.Dir, repo.Config, command)
	if err == nil {
		err = session.AppendEvent(dir, session.GateEvent(h.now(), command,
			h.now().Sub(start), r.Passed, r.Output))
	}
	return r.Passed, r.Output, err
}

// failed handles a batch whose work fails the gate. A commit is never made
// while the gate is failing: the work is stashed, where it stays reachable,
// and each task is retried with more effort or goes to the human (ADR 0005). A merge
// in progress can't be stashed, so it stays for the next attempt.
func (h *Harness) failed(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	wt git.Repo,
	tasks []*queue.Task,
	asked map[string]bool,
	id, how, output string,
) error {
	if !wt.MergeInProgress(ctx) {
		if err := wt.Stash(ctx, "diatom: session "+id+" failed the gate"); err != nil {
			return err
		}
	}
	h.log().Warn("session failed the gate", "session", id, "goal", g.Name)
	for _, t := range tasks {
		if asked[t.ID] {
			if err := repo.Store.Move(g.Name, t, queue.Blocked); err != nil {
				return err
			}
			continue
		}
		reason := fmt.Sprintf("%s with the gate failing, and its work was stashed "+
			"(`git stash list` in the worktree):\n\n```text\n%s\n```", how, output)
		if err := h.escalate(repo, g.Name, t, reason); err != nil {
			return err
		}
	}
	return nil
}

// commit commits the worktree's changes and returns the commit each task's
// work landed in: one commit for the batch, or one fixup per revision. A task
// missing from the map has no commit.
func (h *Harness) commit(
	ctx context.Context,
	repo Repo,
	wt git.Repo,
	dir string,
	kind queue.Kind,
	base string,
	tasks []*queue.Task,
	report session.Report,
) (map[string]string, error) {
	merging := wt.MergeInProgress(ctx)
	staged, err := wt.StageAll(ctx)
	if err != nil {
		return nil, err
	}
	var sha string
	switch {
	case merging:
		sha, err = wt.CommitMerge(ctx)
	case !staged:
	case kind == queue.Revision:
		finished := make([]sessionDone, 0, len(report.Finished))
		for _, e := range report.Finished {
			finished = append(finished, sessionDone{task: e.Task, tree: e.Tree})
		}
		return h.commitFixups(ctx, wt, base, tasks, finished)
	default:
		sha, err = h.commitMessage(ctx, repo, wt, dir, tasks, report.Done)
	}
	if err != nil || sha == "" {
		return nil, err
	}
	shas := map[string]string{}
	for _, t := range tasks {
		shas[t.ID] = sha
	}
	return shas, nil
}

// commitMessage commits what is staged with a message from the
// commit-message profile.
func (h *Harness) commitMessage(
	ctx context.Context,
	repo Repo,
	wt git.Repo,
	dir string,
	tasks []*queue.Task,
	done map[string]bool,
) (string, error) {
	stat, diff, err := wt.StagedDiff(ctx)
	if err != nil {
		return "", err
	}
	var titles []string
	for _, t := range tasks {
		if done[t.ID] {
			titles = append(titles, t.Title)
		}
	}
	if len(titles) == 0 {
		for _, t := range tasks {
			titles = append(titles, t.Title+" (in progress)")
		}
	}
	profile, err := repo.Config.Profile("commit-message")
	if err != nil {
		return "", err
	}
	gen := commitmsg.Generator{Runner: h.Runner, Profile: profile, Check: repo.Config.CommitCheck}
	msg, cost, err := gen.Generate(
		ctx,
		commitmsg.Input{Dir: wt.Dir, Stat: stat, Diff: diff, Titles: titles},
	)
	// Its cost is the session's, whether or not the message came out.
	if cerr := session.UpdateState(
		dir,
		func(st *session.State) { st.CommitCostUSD += cost },
	); cerr != nil {
		h.log().Warn("recording the commit message's cost failed", "dir", dir, "err", cerr)
	}
	if err != nil {
		return "", err
	}
	return wt.Commit(ctx, msg)
}

// settle moves a task on after a session that passed the gate. A task left
// unfinished says how its session ended, how the one after it reads, and
// the retry and the human's question say it again.
func (h *Harness) settle(
	repo Repo,
	goal string,
	t *queue.Task,
	done, asked, waiting bool,
	how string,
) error {
	s := repo.Store
	switch {
	case asked:
		return s.Move(goal, t, queue.Blocked)
	case done:
		return s.Move(goal, t, queue.Done)
	case waiting:
		// It asked for the goal's checks, so it is waiting for an answer, not
		// giving up. applyCI parks it once the work is on the goal's branch.
		return s.Move(goal, t, queue.Pending)
	}
	t.Attempts++
	t.Body = appendSection(t.Body, "Unfinished", how+" before the task was done. The work "+
		"it left passed the gate and was committed; carry on from it.")
	if t.Attempts >= maxIncomplete {
		return h.escalate(
			repo,
			goal,
			t,
			fmt.Sprintf("%d sessions ended without finishing the task, the last: %s.",
				t.Attempts, how),
		)
	}
	return s.Move(goal, t, queue.Pending)
}

// ended says how a session ended, for a task it left unfinished: at the turn
// limit, failing, or with the agent stopping on its own.
func ended(id string, res runner.Result) string {
	switch res.Outcome {
	case runner.TurnLimit:
		return fmt.Sprintf("Session %s hit the turn limit after %d turns", id, res.Turns)
	case runner.Failed:
		return fmt.Sprintf("Session %s failed after %d turns", id, res.Turns)
	}
	return fmt.Sprintf("Session %s ended after %d turns without marking it done", id, res.Turns)
}

// escalate retries a failed task once on the same profile and model with one
// more effort level, the failure added to its text, then parks it as a
// question to the human (ADR 0005).
func (h *Harness) escalate(repo Repo, goal string, t *queue.Task, reason string) error {
	s := repo.Store
	p, err := repo.Config.Profile(t.Profile)
	if err != nil {
		return err
	}
	if !t.Escalated {
		effort := t.Effort
		if effort == "" {
			effort = p.Effort
		}
		t.Effort = config.RetryEffort(effort)
		t.Body = appendSection(t.Body, "Retrying at "+t.Effort+" effort", reason)
		t.Escalated, t.Attempts = true, 0
		return s.Move(goal, t, queue.Pending)
	}
	q := &queue.Question{
		Task:    t.ID,
		Created: h.now(),
		Text: fmt.Sprintf("Task %s (%q) failed on the %s profile. How should it proceed?\n\n%s",
			t.ID, t.Title, t.Profile, reason),
	}
	if err := s.AddQuestion(goal, q); err != nil {
		return err
	}
	t.Attempts = 0
	return s.Move(goal, t, queue.Blocked)
}

// requeue puts a batch's tasks back to pending after a session that could not
// run or finish, except those waiting on a question, and returns cause.
func (h *Harness) requeue(
	s *queue.Store,
	goal string,
	tasks []*queue.Task,
	asked map[string]bool,
	cause error,
) error {
	for _, t := range tasks {
		to := queue.Pending
		t.Attempts++
		switch {
		case asked[t.ID]:
			to = queue.Blocked
		case t.Attempts >= maxIncomplete:
			// The same error again would only repeat: more effort doesn't
			// help with what diatom itself couldn't do.
			t.Attempts = 0
			if err := s.AddQuestion(goal, &queue.Question{
				Task:    t.ID,
				Created: h.now(),
				Text: fmt.Sprintf(
					"Sessions on task %s (%q) keep ending in an error diatom can't get "+
						"past, so it has stopped retrying:\n\n%v\n\nFix what it says, then answer this to try "+
						"again.",
					t.ID,
					t.Title,
					cause,
				),
			}); err != nil {
				return errors.Join(cause, err)
			}
			to = queue.Blocked
		}
		if err := s.Move(goal, t, to); err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}

// noGate is the question a task without a gate to pass waits on.
const noGate = "No gate is configured for this repo, so diatom can't check or commit any work. " +
	"Set `gate` in .diatom/config.toml to the command every commit must pass, such as " +
	"`gate = \"mage ci:check\"`, or set one for every repo of its kind under `[gates]` in " +
	"~/.config/diatom/config.toml, then answer this to carry on."

// askAll blocks each task on a question saying why none of them can run.
func (h *Harness) askAll(s *queue.Store, goal string, tasks []*queue.Task, text string) error {
	for _, t := range tasks {
		if err := s.AddQuestion(
			goal,
			&queue.Question{Task: t.ID, Created: h.now(), Text: text},
		); err != nil {
			return err
		}
		if err := s.Move(goal, t, queue.Blocked); err != nil {
			return err
		}
	}
	h.log().Warn("tasks blocked", "goal", goal, "reason", text)
	return nil
}

// integrate merges a workstream into the integration branch, queueing a
// conflict task when the two have diverged in the same code.
func (h *Harness) integrate(ctx context.Context, repo Repo, g *queue.Goal, ws string) error {
	main := git.Repo{Dir: repo.Store.Repo()}
	unlock := h.lockRepo(main.Dir)
	conflicted, err := main.MergeInto(ctx, g.IntegrationBranch(), g.WorkstreamBranch(ws))
	unlock()
	if err != nil || !conflicted {
		if err == nil {
			h.mirror(ctx, repo, g)
		}
		return err
	}
	return h.addFix(
		repo.Store,
		g.Name,
		ws,
		queue.Conflict,
		"Merging this workstream into the integration branch conflicts, because another workstream "+
			"changed the same code. diatom merges the integration branch into the worktree when this task "+
			"starts: resolve every conflicted file, keeping the intent of both sides.",
	)
}

// usageShare divides a session's usage evenly among its tasks.
func usageShare(session string, u runner.Usage, n int) queue.Usage {
	n = max(n, 1)
	return queue.Usage{
		Session:       session,
		InputTokens:   u.InputTokens / n,
		OutputTokens:  u.OutputTokens / n,
		CacheCreation: u.CacheCreation / n,
		CacheRead:     u.CacheRead / n,
		CostUSD:       u.CostUSD / float64(n),
	}
}

func appendSection(body, heading, text string) string {
	return strings.TrimRight(
		body,
		"\n",
	) + "\n\n## " + heading + "\n\n" + strings.TrimSpace(
		text,
	) + "\n"
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
