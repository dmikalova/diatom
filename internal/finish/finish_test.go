package finish

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// fixture is a repo with a goal whose engine and cards workstreams were
// built the way the harness builds them.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	repo  git.Repo
	store *queue.Store
	goal  *queue.Goal
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), repo: git.Repo{Dir: t.TempDir()}}
	f.git("init", "--initial-branch=main")
	for _, kv := range [][2]string{
		{"user.name", "T"}, {"user.email", "t@example.com"}, {"commit.gpgsign", "false"},
	} {
		f.git("config", kv[0], kv[1])
	}
	f.write(".gitignore", ".diatom/\n")
	f.commitAll("chore: start")
	f.store = queue.Open(f.repo.Dir)
	f.goal = &queue.Goal{
		Name:  "set",
		Title: "New set",
		State: queue.GoalDone,
		Base:  "main",
		Workstreams: []queue.Workstream{
			{Name: "cards", DependsOn: []string{"engine"}},
			{Name: "engine"},
		},
	}
	if err := f.store.CreateGoal(f.goal); err != nil {
		t.Fatal(err)
	}
	f.git("branch", f.goal.IntegrationBranch())
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	out, err := f.repo.Run(f.ctx, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	path := filepath.Join(f.repo.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commitAll(msg string) string {
	f.t.Helper()
	f.git("add", "--all")
	f.git("commit", "--quiet", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

// on checks a branch out, creating it from the integration branch.
func (f *fixture) on(ws string) {
	f.t.Helper()
	branch := f.goal.IntegrationBranch()
	if ws != "" {
		branch = f.goal.WorkstreamBranch(ws)
		if _, err := f.repo.Run(f.ctx, "rev-parse", "--verify", branch); err != nil {
			f.git("branch", branch, f.goal.IntegrationBranch())
		}
	}
	f.git("checkout", "--quiet", branch)
}

// work commits a change on a workstream, first merging the integration
// branch in as the harness does, and integrates it. It returns the commit.
func (f *fixture) work(ws, file, content, msg string) string {
	f.t.Helper()
	f.on(ws)
	f.git("merge", "--quiet", "--no-ff", "--no-edit", f.goal.IntegrationBranch())
	f.write(file, content)
	sha := f.commitAll(msg)
	f.integrate(ws)
	return sha
}

func (f *fixture) integrate(ws string) {
	f.t.Helper()
	f.git("checkout", "--quiet", "main")
	if _, err := f.repo.MergeInto(
		f.ctx,
		f.goal.IntegrationBranch(),
		f.goal.WorkstreamBranch(ws),
	); err != nil {
		f.t.Fatal(err)
	}
}

// task records a done task that made commits.
func (f *fixture) task(ws string, commits ...string) {
	f.t.Helper()
	if err := f.store.AddTask(f.goal.Name, &queue.Task{
		Title:      "work",
		Kind:       queue.Planned,
		Workstream: ws,
		Commits:    commits,
		Created:    time.Now(),
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) subjects(rev string) []string {
	f.t.Helper()
	return strings.Split(f.git("log", "--reverse", "--format=%s", "main.."+rev), "\n")
}

func (f *fixture) build(opts Options) *Result {
	f.t.Helper()
	res, err := Build(f.ctx, f.store, f.goal, opts)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *fixture) sameTree(a, b string) bool {
	f.t.Helper()
	return f.git("rev-parse", a+"^{tree}") == f.git("rev-parse", b+"^{tree}")
}

func TestBuildStacksWorkstreams(t *testing.T) {
	f := newFixture(t)
	// The goal's ADR, committed on the integration branch at sign-off.
	f.on("")
	f.write("docs/adr/0001-ward.md", "# Ward\n")
	adr := f.commitAll("docs: add the ward ADR")
	f.git("checkout", "--quiet", "main")
	ward := f.work("engine", "engine.txt", "ward\n", "feat: add ward")
	// Nobody recorded this one; it is on the cards branch's own line.
	f.work("cards", "cards.txt", "warden\n", "feat: add warden")
	poison := f.work("engine", "poison.txt", "poison\n", "feat: add poison")
	fixup := f.work("engine", "ward_test.txt", "test\n", "fixup! feat: add ward")
	f.task("", adr)
	f.task("engine", ward, poison)
	f.task("engine", fixup)

	var gated []string
	res := f.build(Options{
		Gate: "check",
		RunGate: func(_ context.Context, dir, command string) (gate.Result, error) {
			head, _ := git.Repo{Dir: dir}.RevParse(context.Background(), "HEAD")
			gated = append(gated, command+"@"+head)
			return gate.Result{Passed: true}, nil
		},
	})

	if len(res.Stack) != 2 || res.Unstacked != "" || res.Carried {
		t.Fatalf("result = %+v", res)
	}
	engine, cards := res.Stack[0], res.Stack[1]
	if engine.Branch != "diatom/set/pr/engine" || cards.Branch != "diatom/set/pr/cards" {
		t.Errorf("branches = %s, %s", engine.Branch, cards.Branch)
	}
	if got := f.subjects(engine.Branch); !slices.Equal(
		got,
		[]string{"docs: add the ward ADR", "feat: add ward", "feat: add poison"},
	) {
		t.Errorf("engine pull request = %v", got)
	}
	if got := f.subjects(cards.Branch); len(got) != 4 || got[3] != "feat: add warden" {
		t.Errorf("cards pull request = %v", got)
	}
	// The fixup went into the commit it revises.
	wardCommit := f.git("log", "--format=%H", "--grep=^feat: add ward$", engine.Branch)
	if files := f.git(
		"show",
		"--format=",
		"--name-only",
		wardCommit,
	); files != "engine.txt\nward_test.txt" {
		t.Errorf("ward commit files = %q", files)
	}
	if !f.sameTree(res.Final, f.goal.IntegrationBranch()) ||
		f.git("rev-parse", res.Final) != cards.Tip {
		t.Error("the final branch doesn't hold the integration branch's files")
	}
	if len(gated) != 2 || gated[0] != "check@"+engine.Tip || engine.Gate == nil ||
		!engine.Gate.Passed {
		t.Errorf("gated = %v, engine gate = %+v", gated, engine.Gate)
	}
	if _, err := os.Stat(f.store.WorktreeDir("set", worktreeName)); err == nil {
		t.Error("the replay worktree was left behind")
	}

	saved, err := Load(f.store.GoalDir("set"))
	if err != nil || saved == nil || saved.Tip() != cards.Tip ||
		!Current(f.ctx, f.store, f.goal, saved) {
		t.Errorf("saved = %+v, %v", saved, err)
	}
	desc := Describe(f.goal, res)
	for _, want := range []string{"2. diatom/set/pr/cards: 1 commits, gate passes", "goal finish set -prs"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Describe lacks %q:\n%s", want, desc)
		}
	}
}

func TestBuildFallsBackToOrderOfWork(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "v1\n", "feat: add ward"))
	// Cards changes engine code, and engine then builds on that change, so
	// the engine commits can't all go before the cards one.
	f.task("cards", f.work("cards", "engine.txt", "v1 cards\n", "feat: let cards use ward"))
	f.task("engine", f.work("engine", "engine.txt", "v2 cards\n", "feat: strengthen ward"))
	// A stack from an earlier layout that this one won't have.
	f.git("branch", "diatom/set/pr/old", "main")

	res := f.build(Options{})
	if len(res.Stack) != 1 || res.Stack[0].Branch != res.Final ||
		!strings.Contains(res.Unstacked, "feat: strengthen ward") {
		t.Fatalf("result = %+v", res)
	}
	if got := f.subjects(res.Final); !slices.Equal(
		got,
		[]string{"feat: add ward", "feat: let cards use ward", "feat: strengthen ward"},
	) {
		t.Errorf("commits = %v", got)
	}
	if f.repo.BranchExists(f.ctx, "diatom/set/pr/old") {
		t.Error("a stale pull request branch was kept")
	}
	if !strings.Contains(Describe(f.goal, res), "one pull request in the order the work was done") {
		t.Error("Describe doesn't say why there's no stack")
	}
}

func TestBuildCarriesMergeFixes(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.task("cards", f.work("cards", "cards.txt", "warden\n", "feat: add warden"))
	// A gate-repair task fixed the merge of the integration branch into
	// engine, so the fix is in the merge commit.
	f.on("engine")
	f.git("merge", "--quiet", "--no-ff", "--no-commit", f.goal.IntegrationBranch())
	f.write("cards.txt", "warden, fixed\n")
	f.git("add", "--all")
	f.git("commit", "--quiet", "--no-edit")
	f.integrate("engine")

	res := f.build(Options{})
	if !res.Carried || !f.sameTree(res.Final, f.goal.IntegrationBranch()) {
		t.Fatalf("result = %+v", res)
	}
	if got := f.subjects(res.Final); got[len(got)-1] != firstLine(carryMessage) {
		t.Errorf("commits = %v", got)
	}
}

func TestBuildOntoAMovedBase(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.git("checkout", "--quiet", "main")
	f.write("readme.txt", "hi\n")
	f.commitAll("docs: add a readme")

	res := f.build(Options{})
	if res.Carried || f.git("rev-parse", "main") != f.git("rev-parse", res.Final+"~1") {
		t.Errorf("not laid out on main's tip: %+v", res)
	}
	if !Current(f.ctx, f.store, f.goal, res) {
		t.Error("a fresh layout isn't current")
	}

	f.write("engine.txt", "not ward\n")
	f.commitAll("feat: something else")
	if Current(f.ctx, f.store, f.goal, res) {
		t.Error("still current after main moved")
	}
	if _, err := Build(f.ctx, f.store, f.goal, Options{}); err == nil ||
		!strings.Contains(err.Error(), "main has moved on and conflicts") {
		t.Errorf("Build onto a conflicting base = %v", err)
	}
}

func TestPushAndOpenPRs(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.task("cards", f.work("cards", "cards.txt", "warden\n", "feat: add warden"))
	res := f.build(Options{})

	remote := t.TempDir()
	if _, err := (git.Repo{Dir: remote}).Run(
		f.ctx,
		"init",
		"--bare",
		"--initial-branch=main",
	); err != nil {
		t.Fatal(err)
	}
	f.git("remote", "add", "origin", remote)
	f.git("push", "--quiet", "origin", "main")

	var calls []string
	open := map[string]bool{"diatom/set/pr/engine": true}
	gh := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args[:min(len(args), 6)], " "))
		switch args[1] {
		case "view":
			if open[args[2]] {
				return "https://example.com/pull/1", nil
			}
			return "", os.ErrNotExist
		case "create":
			return "Creating…\nhttps://example.com/pull/2", nil
		}
		return "", nil
	}
	urls, err := OpenPRs(f.ctx, f.store, f.goal, res, "origin", gh)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(urls, []string{"https://example.com/pull/1", "https://example.com/pull/2"}) {
		t.Errorf("urls = %v", urls)
	}
	wantCalls := []string{
		"pr view diatom/set/pr/engine --json url --jq",
		"pr edit diatom/set/pr/engine --base main",
		"pr view diatom/set/pr/cards --json url --jq",
		"pr create --head diatom/set/pr/cards --base diatom/set/pr/engine",
	}
	if !slices.Equal(calls, wantCalls) {
		t.Errorf("gh calls:\n%s", strings.Join(calls, "\n"))
	}
	bare := git.Repo{Dir: remote}
	if tip, _ := bare.RevParse(f.ctx, "diatom/set/pr/cards"); tip != res.Tip() {
		t.Error("the cards branch wasn't pushed")
	}
	if body := prBody(
		f.goal,
		res,
		1,
		urls,
	); !strings.Contains(
		body,
		"stacked on https://example.com/pull/1",
	) {
		t.Errorf("body = %s", body)
	}

	if err := push(f.ctx, f.store, f.goal, res, "origin"); err != nil {
		t.Fatal(err)
	}
	if tip, _ := bare.RevParse(f.ctx, "main"); tip != res.Tip() {
		t.Error("main wasn't pushed")
	}
	// Once main on the remote has moved on, the layout's push is refused,
	// never forced.
	f.git("checkout", "--quiet", "--detach", res.Tip())
	f.write("readme.txt", "hi\n")
	f.commitAll("docs: add a readme")
	f.git("push", "--quiet", "origin", "HEAD:main")
	if err := push(f.ctx, f.store, f.goal, res, "origin"); err == nil ||
		!strings.Contains(err.Error(), "main on origin has moved on") {
		t.Errorf("push onto a moved main = %v", err)
	}
}
