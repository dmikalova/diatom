package harness

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

// agent stands in for a coding agent: act runs in the session with its
// worktree and session directory.
type agent struct {
	mu       sync.Mutex
	last     runner.Spec
	act      func(t *testing.T, wt string, s agentSession)
	sessions int
	t        *testing.T
}

// agentSession is what a fake agent can do in its session.
type agentSession struct {
	dir  string
	spec session.Spec
	t    *testing.T
}

func (s agentSession) report(typ, task, text string) {
	s.t.Helper()
	if err := session.Append(
		s.dir,
		s.spec,
		session.Entry{Type: typ, Task: task, Text: text},
	); err != nil {
		s.t.Fatal(err)
	}
}

func (a *agent) Run(
	_ context.Context,
	spec runner.Spec,
	_ func(runner.Event),
) (runner.Result, error) {
	if !slices.ContainsFunc(
		spec.Env,
		func(e string) bool { return strings.HasPrefix(e, session.EnvVar+"=") },
	) {
		// The commit-message call, the only one outside a session.
		return runner.Result{Text: "Sure! Here it is:\nfeat: do the work\n"}, nil
	}
	a.mu.Lock()
	a.sessions++
	a.last = spec
	a.mu.Unlock()
	var dir string
	for _, e := range spec.Env {
		if v, ok := strings.CutPrefix(e, session.EnvVar+"="); ok {
			dir = v
		}
	}
	s, err := session.Load(dir)
	if err != nil {
		return runner.Result{}, err
	}
	a.act(a.t, spec.Dir, agentSession{dir: dir, spec: s, t: a.t})
	return runner.Result{
		Outcome: runner.Completed,
		Usage:   runner.Usage{InputTokens: 100, CostUSD: 1},
	}, nil
}

type fixture struct {
	t       *testing.T
	h       *Harness
	agent   *agent
	store   *queue.Store
	main    git.Repo
	gateOK  func(dir string) bool
	mu      sync.Mutex
	gateRan int
}

// newFixture makes a repo with one commit on main, a goal with the engine and
// cards workstreams, and a harness over it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	main := git.Repo{Dir: repo}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := main.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, repo, ".git/info/exclude", ".diatom/\n")
	writeFile(t, repo, "shared.txt", "base\n")
	if _, err := main.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := main.Commit(ctx, "chore: start"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".diatom/config.yaml", "gate: check\nmaxSessions: 2\n")

	store := queue.Open(repo)
	if err := store.CreateGoal(
		&queue.Goal{
			Name:        "set",
			Title:       "Implement the set",
			State:       queue.GoalActive,
			Base:        "main",
			Workstreams: []queue.Workstream{{Name: "engine"}, {Name: "cards"}},
		},
	); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	f := &fixture{t: t, store: store, main: main, gateOK: func(string) bool { return true }}
	f.agent = &agent{t: t}
	f.h = &Harness{
		Paths:  config.Paths{Home: base, XDG: filepath.Join(base, "xdg")},
		Repos:  func() ([]string, error) { return []string{repo}, nil },
		Runner: f.agent,
		Exe:    "/usr/bin/true",
		Gate: func(_ context.Context, dir, _ string) (gate.Result, error) {
			f.mu.Lock()
			f.gateRan++
			ok := f.gateOK(dir)
			f.mu.Unlock()
			if ok {
				return gate.Result{Passed: true}, nil
			}
			return gate.Result{Output: "FAIL"}, nil
		},
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Poll: 10 * time.Millisecond,
	}
	return f
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) add(ws, title string, deps ...string) *queue.Task {
	f.t.Helper()
	task := &queue.Task{
		Title:      title,
		Kind:       queue.Planned,
		Workstream: ws,
		DependsOn:  deps,
		Origin:     queue.Origin{Type: "plan"},
	}
	if err := f.store.AddTask("set", task); err != nil {
		f.t.Fatal(err)
	}
	return task
}

// step plans once and runs every batch that comes back at the same time, as
// the loop does.
func (f *fixture) step() []schedule.Batch {
	f.t.Helper()
	ctx := context.Background()
	batches, repos, err := f.h.plan(ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, b := range batches {
		wg.Go(func() {
			if err := f.h.RunBatch(ctx, repos[b.Repo], b); err != nil {
				f.t.Errorf("RunBatch %+v: %v", b, err)
			}
		})
	}
	wg.Wait()
	return batches
}

// barrier returns a function that blocks until n callers have reached it.
func barrier(n int) func() {
	var wg sync.WaitGroup
	wg.Add(n)
	return func() {
		wg.Done()
		wg.Wait()
	}
}

func (f *fixture) task(id string) *queue.Task {
	f.t.Helper()
	task, err := f.store.Task("set", id)
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

func (f *fixture) show(rev, path string) string {
	f.t.Helper()
	out, err := f.main.Run(context.Background(), "show", rev+":"+path)
	if err != nil {
		return ""
	}
	return out
}

func TestBatchCommitsAndIntegrates(t *testing.T) {
	f := newFixture(t)
	a := f.add("engine", "Add ward")
	b := f.add("engine", "Add poison")
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward\n")
		s.report(session.EntryNote, a.ID, "Ward lives in ward.txt.")
		s.report(session.EntryDone, a.ID, "")
		s.report(session.EntryDone, b.ID, "")
	}
	if got := f.step(); len(got) != 1 || len(got[0].Tasks) != 2 {
		t.Fatalf("batches = %+v, want both engine tasks in one", got)
	}

	for _, id := range []string{a.ID, b.ID} {
		task := f.task(id)
		if task.State != queue.Done || len(task.Commits) != 1 || len(task.Usage) != 1 ||
			task.Usage[0].CostUSD != 0.5 {
			t.Errorf("task %s = %+v", id, task)
		}
	}
	if !strings.Contains(f.task(a.ID).Body, "Ward lives in ward.txt.") {
		t.Errorf("note missing from body %q", f.task(a.ID).Body)
	}
	if got := f.show("diatom/set/integration", "ward.txt"); got != "ward" {
		t.Errorf("integration ward.txt = %q", got)
	}
	msg, _ := f.main.Run(context.Background(), "log", "-1", "--format=%B", "diatom/set/ws/engine")
	if msg != "feat: do the work" {
		t.Errorf("commit message = %q, want the preface stripped", msg)
	}
	if f.gateRan != 1 {
		t.Errorf("gate ran %d times, want once", f.gateRan)
	}
	if got := f.step(); len(got) != 0 {
		t.Errorf("second step started %+v", got)
	}
}

func TestDependenciesWait(t *testing.T) {
	f := newFixture(t)
	first := f.add("engine", "Engine hook")
	second := f.add("cards", "Card using the hook", first.ID)
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		for _, id := range s.spec.Tasks {
			writeFile(t, wt, s.spec.Workstream+".txt", id)
			s.report(session.EntryDone, id, "")
		}
	}
	if got := f.step(); len(got) != 1 || got[0].Workstream != "engine" {
		t.Fatalf("first step = %+v", got)
	}
	if got := f.step(); len(got) != 1 || got[0].Workstream != "cards" {
		t.Fatalf("second step = %+v", got)
	}
	// cards merged the integration branch before its task, so it saw engine's
	// work, and integration now has both.
	if f.show("diatom/set/ws/cards", "engine.txt") == "" ||
		f.show("diatom/set/integration", "cards.txt") != second.ID {
		t.Error("downstream workstream did not build on upstream work")
	}
}

func TestQuestionParksAndAnswerResumes(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.report(session.EntryAsk, task.ID, "Does ward stack?")
	}
	f.step()
	if got := f.task(task.ID); got.State != queue.Blocked {
		t.Fatalf("task state = %s, want blocked", got.State)
	}
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if len(open) != 1 || !strings.Contains(open[0].Text, "Does ward stack?") {
		t.Fatalf("open questions = %+v", open)
	}
	if got := f.step(); len(got) != 0 {
		t.Errorf("a blocked task was scheduled: %+v", got)
	}

	if err := f.store.Answer("set", open[0].ID, "No.", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.agent.act = func(_ *testing.T, _ string, s agentSession) { s.report(session.EntryDone, task.ID, "") }
	if got := f.step(); len(got) != 1 {
		t.Fatalf("answered task not scheduled: %+v", got)
	}
	got := f.task(task.ID)
	if got.State != queue.Done || !strings.Contains(got.Body, "## Answer 0001\n\nNo.") {
		t.Errorf("task after answer = %s %q", got.State, got.Body)
	}
}

func TestGateFailureEscalatesThenAsks(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	f.gateOK = func(string) bool { return false }
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "broken.txt", "x\n")
		s.report(session.EntryDone, task.ID, "")
	}
	f.step()
	got := f.task(task.ID)
	if got.State != queue.Pending || got.Profile != "implementation" || got.Effort != "high" ||
		!got.Escalated ||
		!strings.Contains(got.Body, "FAIL") {
		t.Fatalf(
			"after the first failure = %+v, want pending on the same profile at high effort, with the failure",
			got,
		)
	}
	wt := git.Repo{Dir: f.store.WorktreeDir("set", "engine")}
	if dirty, _ := wt.Dirty(context.Background()); dirty {
		t.Error("failed work was left in the worktree")
	}
	if stash, _ := wt.Run(
		context.Background(),
		"stash",
		"list",
	); !strings.Contains(
		stash,
		"failed the gate",
	) {
		t.Errorf("stash list = %q", stash)
	}
	if head, _ := f.main.RevParse(
		context.Background(),
		"diatom/set/ws/engine",
	); head != mustRev(
		t,
		f.main,
		"main",
	) {
		t.Error("a commit was made while the gate failed")
	}

	f.step()
	if got := f.agent.last.Profile; got.Model != "opus" || got.Effort != "high" {
		t.Errorf(
			"retry ran on %s at %s effort, want the same model one level up",
			got.Model,
			got.Effort,
		)
	}
	got = f.task(task.ID)
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if got.State != queue.Blocked || len(open) != 1 || !strings.Contains(open[0].Text, "FAIL") {
		t.Errorf("after the second failure: task %s, questions %+v", got.State, open)
	}
}

func mustRev(t *testing.T, r git.Repo, rev string) string {
	t.Helper()
	sha, err := r.RevParse(context.Background(), rev)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestHookPassSkipsSecondGate(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward\n")
		fp, err := git.Repo{Dir: wt}.Fingerprint(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := session.SaveGate(s.dir, session.GateState{Passed: fp}); err != nil {
			t.Fatal(err)
		}
		s.report(session.EntryDone, task.ID, "")
	}
	f.step()
	if f.gateRan != 0 || f.task(task.ID).State != queue.Done {
		t.Errorf("gate ran %d times after the hook passed it", f.gateRan)
	}
}

func TestIncompleteTaskEscalates(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	f.agent.act = func(t *testing.T, wt string, _ agentSession) { writeFile(t, wt, "partial.txt", "wip\n") }
	f.step()
	if got := f.task(
		task.ID,
	); got.State != queue.Pending || got.Attempts != 1 ||
		len(got.Commits) != 1 {
		t.Fatalf(
			"after one incomplete session = %+v, want pending with the partial work committed",
			got,
		)
	}
	f.step()
	if got := f.task(
		task.ID,
	); got.Profile != "implementation" || got.Effort != "high" ||
		got.Attempts != 0 {
		t.Errorf("after two incomplete sessions = %+v, want retried at high effort", got)
	}
}

func TestConflictBetweenWorkstreams(t *testing.T) {
	f := newFixture(t)
	engine := f.add("engine", "Engine edit")
	cards := f.add("cards", "Cards edit")
	// Both sessions start before either lands, as two concurrent
	// workstreams would.
	together := barrier(2)
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		task, err := f.store.Task("set", s.spec.Tasks[0])
		if err != nil {
			t.Fatal(err)
		}
		switch task.Kind {
		case queue.Conflict:
			if !(git.Repo{Dir: wt}).MergeInProgress(context.Background()) {
				t.Error("conflict session started without the merge in progress")
			}
			writeFile(t, wt, "shared.txt", "both\n")
		default:
			together()
			writeFile(t, wt, "shared.txt", s.spec.Workstream+"\n")
		}
		for _, id := range s.spec.Tasks {
			s.report(session.EntryDone, id, "")
		}
	}
	// Both workstreams run in the same step, off the same integration commit.
	if got := f.step(); len(got) != 2 {
		t.Fatalf("first step = %+v", got)
	}
	if f.task(engine.ID).State != queue.Done || f.task(cards.ID).State != queue.Done {
		t.Fatal("the edits did not land")
	}
	tasks, _ := f.store.Tasks("set")
	var conflict *queue.Task
	for _, task := range tasks {
		if task.Kind == queue.Conflict {
			conflict = task
		}
	}
	if conflict == nil {
		t.Fatalf("tasks = %+v, want a conflict task for the workstream that landed second", tasks)
	}

	if got := f.step(); len(got) != 1 || got[0].Kind != queue.Conflict {
		t.Fatalf("conflict step = %+v", got)
	}
	if got := f.task(conflict.ID); got.State != queue.Done {
		t.Errorf("conflict task = %s", got.State)
	}
	if got := f.show("diatom/set/integration", "shared.txt"); got != "both" {
		t.Errorf("integration shared.txt = %q, want the resolution", got)
	}
}

func TestMergeFailingGateQueuesRepair(t *testing.T) {
	f := newFixture(t)
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		for _, id := range s.spec.Tasks {
			task, err := f.store.Task("set", id)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, wt, task.Title+".txt", "x\n")
			s.report(session.EntryDone, id, "")
		}
	}
	// Both worktrees exist before engine lands its second change.
	f.add("engine", "engine1")
	f.add("cards", "cards1")
	f.step()
	f.add("engine", "engine2")
	f.step()

	// engine2 and cards pass alone but fail together, until fixed.
	f.add("cards", "cards2")
	f.gateOK = func(dir string) bool {
		return filepath.Base(dir) != "cards" || !fileExists(filepath.Join(dir, "engine2.txt")) ||
			fileExists(filepath.Join(dir, "fixed.txt"))
	}
	f.step()
	tasks, _ := f.store.Tasks("set")
	var repair, cards2 *queue.Task
	for _, task := range tasks {
		switch {
		case task.Kind == queue.GateRepair:
			repair = task
		case task.Title == "cards2":
			cards2 = task
		}
	}
	if repair == nil || !strings.Contains(repair.Body, "FAIL") {
		t.Fatalf("tasks = %+v, want a gate repair with the output", tasks)
	}
	if cards2.State != queue.Pending {
		t.Errorf("cards2 = %s, want it left pending behind the repair", cards2.State)
	}
	wt := git.Repo{Dir: f.store.WorktreeDir("set", "cards")}
	if !wt.MergeInProgress(context.Background()) {
		t.Fatal("the failing merge was not left in progress")
	}

	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "fixed.txt", "x\n")
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	if got := f.step(); len(got) != 1 || got[0].Kind != queue.GateRepair {
		t.Fatalf("repair step = %+v", got)
	}
	if wt.MergeInProgress(context.Background()) ||
		f.show("diatom/set/integration", "fixed.txt") == "" {
		t.Error("the repaired merge was not committed and integrated")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRecover(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	if err := f.store.Move("set", task, queue.Active); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.task(task.ID); got.State != queue.Pending {
		t.Errorf("recovered task = %s", got.State)
	}
}

func TestRunLoop(t *testing.T) {
	f := newFixture(t)
	task := f.add("engine", "Add ward")
	stop, cancel := context.WithCancel(context.Background())
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward\n")
		s.report(session.EntryDone, task.ID, "")
		cancel()
	}
	done := make(chan error)
	go func() { done <- f.h.Run(stop, context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
	if got := f.task(task.ID); got.State != queue.Done {
		t.Errorf("task = %s, want done: Run should wait for the running session", got.State)
	}
}

func TestPrompt(t *testing.T) {
	p := Prompt(PromptInput{
		Goal: &queue.Goal{Title: "Implement the set"},
		Batch: schedule.Batch{
			Workstream: "engine",
			Kind:       queue.Conflict,
			Tasks:      []*queue.Task{{ID: "0001", Title: "Fix", Body: "Keep both sides.\n"}},
		},
		Gate:    "mage check",
		TaskDir: "/r/tasks/active",
	})
	for _, want := range []string{"1 task", "`mage check`", "diatom task done", "A merge is in progress", "/r/tasks/active/0001.md", "### Task 0001: Fix", "Keep both sides."} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestSessionContext(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	home := f.h.Paths.Home
	writeFile(t, home, "AGENTS.md", "Be terse.\n")
	writeFile(t, home, ".claude/skills/grilling/SKILL.md", "---\nname: grilling\n---\n")
	writeFile(
		t,
		f.h.Paths.XDG,
		"config.yaml",
		"profiles:\n  implementation:\n    skills: [grilling]\n",
	)
	writeFile(
		t,
		f.main.Dir,
		".diatom/config.yaml",
		"gate: check\nmcpServers:\n  docs:\n    command: docs-mcp\n",
	)
	writeFile(t, f.main.Dir, "AGENTS.md", "Run mage.\n")
	writeFile(t, f.main.Dir, "internal/cards/AGENTS.md", "Card rules.\n")
	if _, err := f.main.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.main.Commit(ctx, "docs: add agent instructions"); err != nil {
		t.Fatal(err)
	}

	task := f.add("engine", "Add ward")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) { s.report(session.EntryDone, task.ID, "") }
	f.step()

	spec := f.agent.last
	if !strings.Contains(spec.Instructions, "Be terse.") ||
		!strings.Contains(spec.Instructions, "Run mage.") ||
		strings.Index(
			spec.Instructions,
			"Be terse.",
		) > strings.Index(
			spec.Instructions,
			"Run mage.",
		) {
		t.Errorf("instructions = %q, want ~/AGENTS.md then the repo's", spec.Instructions)
	}
	if len(spec.Skills) != 1 ||
		spec.Skills[0] != filepath.Join(home, ".claude", "skills", "grilling") {
		t.Errorf("skills = %v", spec.Skills)
	}
	if _, ok := spec.MCPServers["docs"]; !ok {
		t.Errorf("MCP servers = %v", spec.MCPServers)
	}
	if !strings.Contains(spec.Prompt, "`internal/cards/AGENTS.md`") ||
		strings.Contains(spec.Prompt, "- `AGENTS.md`") {
		t.Errorf("prompt does not list the nested guide alone:\n%s", spec.Prompt)
	}
}
