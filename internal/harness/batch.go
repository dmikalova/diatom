package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/commitmsg"
	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
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
func (h *Harness) RunBatch(ctx context.Context, repo Repo, b schedule.Batch) error {
	s := repo.Store
	g, err := s.Goal(b.Goal)
	if err != nil {
		return err
	}
	main := git.Repo{Dir: s.Repo()}
	wt := git.Repo{Dir: s.WorktreeDir(g.Name, b.Workstream)}
	unlock := h.lockRepo(s.Repo())
	err = main.CreateBranch(ctx, g.IntegrationBranch(), g.Base)
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
	res, runErr := h.runSession(ctx, repo, g, b, wt, dir, spec)
	if err := session.WriteResult(
		dir,
		sessionResult{Result: res, Error: errString(runErr)},
	); err != nil {
		h.log().Warn("writing the session result failed", "session", spec.ID, "err", err)
	}
	return h.finish(ctx, repo, g, b, wt, dir, res, runErr)
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
// and reports whether the batch can run. A merge that conflicts or fails the
// gate is left in progress for a conflict or gate-repair task, which runs
// first because it has the higher priority (ADR 0004).
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
	res, err := wt.MergeNoCommit(ctx, g.IntegrationBranch())
	if err != nil {
		return false, err
	}
	switch res {
	case git.Conflicted:
		if fixing {
			return true, nil
		}
		return false, h.addFix(
			repo.Store,
			g.Name,
			b.Workstream,
			queue.Conflict,
			"Merging the integration branch into this workstream conflicts. The merge is in progress in the "+
				"worktree: resolve every conflicted file, keeping the intent of both sides.",
		)
	case git.Merged:
		r, err := h.runGate(ctx, wt.Dir, repo.Config.Gate)
		if err != nil {
			return false, err
		}
		if !r.Passed {
			if fixing {
				return true, nil
			}
			return false, h.addFix(
				repo.Store,
				g.Name,
				b.Workstream,
				queue.GateRepair,
				"Merging the integration branch into this workstream fails the gate. The merge is in progress in "+
					"the worktree: make the gate pass.\n\n```text\n"+r.Output+"\n```",
			)
		}
		if _, err := wt.CommitMerge(ctx); err != nil {
			return false, err
		}
	}
	if b.Kind == queue.Conflict {
		// The merge went through cleanly, so nothing is left to resolve.
		for _, t := range b.Tasks {
			t.Body = appendSection(
				t.Body,
				"Resolved",
				"The integration branch merged into the workstream without conflicts.",
			)
			if err := repo.Store.Move(g.Name, t, queue.Done); err != nil {
				return false, err
			}
		}
		return false, h.integrate(ctx, repo, g, b.Workstream)
	}
	return true, nil
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
		Kind: b.Kind, Profile: b.Profile, Gate: cfg.Gate, GateAttempts: cfg.GateAttempts,
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
	prompt := Prompt(PromptInput{
		Goal: g, Batch: b, Gate: cfg.Gate,
		TaskDir: filepath.Join(s.GoalDir(g.Name), "tasks", string(queue.Active)),
		Merging: wt.MergeInProgress(ctx),
		Guides:  guides,
	})
	return dir, spec, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0o644)
}

// sessionDir makes a new session directory named for the time and the
// workstream.
func (h *Harness) sessionDir(root, ws string) (id, dir string, err error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
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

// runSession runs the agent, logging its events to events.jsonl.
func (h *Harness) runSession(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	wt git.Repo,
	dir string,
	spec session.Spec,
) (runner.Result, error) {
	profile, err := repo.Config.Profile(b.Profile)
	if err != nil {
		return runner.Result{}, err
	}
	if b.Effort != "" {
		profile.Effort = b.Effort
	}
	instructions, err := h.instructions(repo.Config, wt)
	if err != nil {
		return runner.Result{}, err
	}
	var skills []string
	for _, sk := range profile.Skills {
		skills = append(skills, h.Paths.SkillDir(sk))
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt.md"))
	if err != nil {
		return runner.Result{}, err
	}
	events, err := os.Create(filepath.Join(dir, "events.jsonl"))
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
	h.log().Info("session starting", "session", spec.ID, "goal", g.Name, "workstream", b.Workstream,
		"kind", b.Kind, "profile", b.Profile, "tasks", spec.Tasks)
	res, err := h.Runner.Run(ctx, runner.Spec{
		Dir:     wt.Dir,
		AddDirs: []string{filepath.Join(repo.Store.GoalDir(g.Name), "tasks", string(queue.Active))},
		Prompt:  string(prompt),
		Profile: profile,
		Env: []string{
			session.EnvVar + "=" + dir,
			"PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		},
		Hooks: runner.Hooks{
			PreToolUse: exe + " hook pre-tool-use",
			Stop:       exe + " hook stop",
		},
		Instructions: instructions,
		Skills:       skills,
		MCPServers:   repo.Config.MCPServers,
	}, onEvent)
	h.log().Info("session ended", "session", spec.ID, "outcome", res.Outcome, "turns", res.Turns,
		"costUSD", res.Usage.CostUSD, "err", err)
	return res, err
}

// instructions gathers what is appended to the agent's system prompt: the
// configured instruction files, such as ~/AGENTS.md, then the worktree's root
// AGENTS.md. Claude Code reads neither on its own, and the session loads no
// other user context (ADR 0006).
func (h *Harness) instructions(cfg *config.Config, wt git.Repo) (string, error) {
	var parts []string
	paths := make([]string, 0, len(cfg.Instructions)+1)
	for _, p := range cfg.Instructions {
		paths = append(paths, h.Paths.Expand(p))
	}
	for _, p := range append(paths, filepath.Join(wt.Dir, "AGENTS.md")) {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		parts = append(
			parts,
			fmt.Sprintf("# Instructions from %s\n\n%s", p, string(bytes.TrimSpace(b))),
		)
	}
	return strings.Join(parts, "\n\n"), nil
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
	report, err := session.ReadReport(dir)
	if err != nil {
		return err
	}
	for _, n := range report.Notes {
		if err := s.AppendNote(g.Name, n.Task, "Note", n.Text); err != nil {
			return err
		}
	}
	tasks, err := h.reload(s, g.Name, b.Tasks)
	if err != nil {
		return err
	}
	asked := map[string]bool{}
	for _, q := range report.Questions {
		if err := s.AddQuestion(
			g.Name,
			&queue.Question{Task: q.Task, Text: q.Text, Created: h.now()},
		); err != nil {
			return err
		}
		asked[q.Task] = true
	}
	if runErr != nil {
		return h.requeue(s, g.Name, tasks, asked, runErr)
	}

	st, err := session.LoadGate(dir)
	if err != nil {
		return err
	}
	passed, output, err := h.check(ctx, repo, wt, st.Passed)
	if err != nil {
		return h.requeue(s, g.Name, tasks, asked, err)
	}
	if !passed {
		return h.failed(ctx, repo, g, wt, tasks, asked, filepath.Base(dir), output)
	}

	shas, err := h.commit(ctx, repo, wt, b.Kind, g.Base, tasks, report)
	if err != nil {
		return h.requeue(s, g.Name, tasks, asked, err)
	}
	share := usageShare(filepath.Base(dir), res.Usage, len(tasks))
	for _, t := range tasks {
		if sha := shas[t.ID]; sha != "" {
			t.Commits = append(t.Commits, sha)
		}
		t.Usage = append(t.Usage, share)
		if err := h.settle(repo, g.Name, t, report.Done[t.ID], asked[t.ID]); err != nil {
			return err
		}
	}
	return h.integrate(ctx, repo, g, b.Workstream)
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
	hookPassed string,
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
	r, err := h.runGate(ctx, wt.Dir, repo.Config.Gate)
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
	id, output string,
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
		reason := fmt.Sprintf("Session %s ended with the gate failing, and its work was stashed "+
			"(`git stash list` in the worktree):\n\n```text\n%s\n```", id, output)
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
		sha, err = h.commitMessage(ctx, repo, wt, tasks, report.Done)
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
	msg, err := gen.Generate(
		ctx,
		commitmsg.Input{Dir: wt.Dir, Stat: stat, Diff: diff, Titles: titles},
	)
	if err != nil {
		return "", err
	}
	return wt.Commit(ctx, msg)
}

// settle moves a task on after a session that passed the gate.
func (h *Harness) settle(repo Repo, goal string, t *queue.Task, done, asked bool) error {
	s := repo.Store
	switch {
	case asked:
		return s.Move(goal, t, queue.Blocked)
	case done:
		return s.Move(goal, t, queue.Done)
	}
	t.Attempts++
	if t.Attempts >= maxIncomplete {
		return h.escalate(
			repo,
			goal,
			t,
			fmt.Sprintf("%d sessions ended without finishing the task.", t.Attempts),
		)
	}
	return s.Move(goal, t, queue.Pending)
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
		if asked[t.ID] {
			to = queue.Blocked
		}
		if err := s.Move(goal, t, to); err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}

// integrate merges a workstream into the integration branch, queueing a
// conflict task when the two have diverged in the same code.
func (h *Harness) integrate(ctx context.Context, repo Repo, g *queue.Goal, ws string) error {
	main := git.Repo{Dir: repo.Store.Repo()}
	unlock := h.lockRepo(main.Dir)
	conflicted, err := main.MergeInto(ctx, g.IntegrationBranch(), g.WorkstreamBranch(ws))
	unlock()
	if err != nil || !conflicted {
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
