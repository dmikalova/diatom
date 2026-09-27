package finish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestCatchUp(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.git("checkout", "--quiet", "main")

	// Up to date already: nothing to do.
	if up, err := CatchUp(f.ctx, f.store, f.goal, "", time.Unix(1, 0)); err != nil || !up {
		t.Fatalf("an up-to-date goal: %v, %v", up, err)
	}

	// main moves on elsewhere: the goal merges it in straight away.
	f.write("readme.txt", "hi\n")
	f.commitAll("docs: add a readme")
	if up, err := CatchUp(f.ctx, f.store, f.goal, "", time.Unix(2, 0)); err != nil || !up {
		t.Fatalf("a clean catch-up: %v, %v", up, err)
	}
	if ok, _ := f.repo.IsAncestor(f.ctx, "main", f.goal.IntegrationBranch()); !ok {
		t.Error("the integration branch doesn't hold main")
	}

	// main moves on in the goal's own code: an agent merges it in, and the
	// done goal is active again until then.
	f.write("engine.txt", "not ward\n")
	f.commitAll("feat: something else")
	for range 2 {
		if up, err := CatchUp(f.ctx, f.store, f.goal, "", time.Unix(3, 0)); err != nil || up {
			t.Fatalf("a conflicting catch-up: %v, %v", up, err)
		}
	}
	tasks, _ := f.store.Tasks("set")
	var merges []*queue.Task
	for _, task := range tasks {
		if task.Merge != "" {
			merges = append(merges, task)
		}
	}
	if len(merges) != 1 || merges[0].Kind != queue.Conflict || merges[0].Merge != "main" ||
		merges[0].Workstream != "cards" || !strings.Contains(merges[0].Body, "reviewed before the goal lands") {
		t.Errorf("merge tasks = %+v", merges)
	}
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Errorf("the goal is %s", g.State)
	}
}

func TestBaseTipFollowsUpstreamWhenAhead(t *testing.T) {
	f := newFixture(t)
	if got := BaseTip(f.ctx, f.repo, f.goal); got != "main" {
		t.Errorf("with no upstream: %q", got)
	}
	bare := t.TempDir()
	f.git("init", "--quiet", "--bare", bare)
	f.git("remote", "add", "origin", bare)
	f.git("push", "--quiet", "-u", "origin", "main")
	f.write("readme.txt", "hi\n")
	f.git("add", "-A")
	f.git("commit", "--quiet", "-m", "docs: readme")
	f.git("push", "--quiet", "origin", "main")
	f.git("reset", "--quiet", "--hard", "HEAD~1")
	if got := BaseTip(f.ctx, f.repo, f.goal); got != "origin/main" {
		t.Errorf("behind its upstream: %q", got)
	}
}

func TestBuildRebasesOntoAConflictingBase(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.task("cards", f.work("cards", "cards.txt", "warden\n", "feat: add warden"))
	// main changes the goal's own file, and the catch-up's resolution lands
	// on the integration branch, as an agent's would.
	f.git("checkout", "--quiet", "main")
	f.write("engine.txt", "not ward\n")
	f.commitAll("feat: something else")
	f.on("")
	if _, err := f.repo.Run(f.ctx, "merge", "--no-edit", "main"); err == nil {
		t.Fatal("the merge didn't conflict")
	}
	f.write("engine.txt", "ward, not ward\n")
	f.git("add", "-A")
	f.git("commit", "--quiet", "--no-edit")
	f.git("checkout", "--quiet", "main")

	var progress strings.Builder
	res := f.build(Options{Progress: &progress})
	if !res.Rebased || res.Squashed || !res.Carried || len(res.Stack) != 2 {
		t.Fatalf("laid out as %+v", res)
	}
	tip := res.Tip()
	// The goal's own commits, on main's tip with no merge, then the reviewed
	// resolution.
	if merges := f.git("rev-list", "--merges", "main.."+tip); merges != "" {
		t.Errorf("merges on the way: %s", merges)
	}
	if ok, _ := f.repo.IsAncestor(f.ctx, "main", tip); !ok {
		t.Error("the commits aren't on main's tip")
	}
	want := []string{
		"feat: add ward",
		"feat: add warden",
		"fix: keep the reviewed resolution of main's changes",
	}
	if got := f.subjects(tip); strings.Join(got[len(got)-3:], "|") != strings.Join(want, "|") {
		t.Errorf("subjects = %v", got)
	}
	if !f.sameTree(tip, f.goal.IntegrationBranch()) {
		t.Error("the tip doesn't hold the reviewed resolution")
	}
	if !strings.Contains(progress.String(), "rebasing them onto it") {
		t.Errorf("progress:\n%s", progress.String())
	}
}

func TestBuildRebasesWhatNoLineSettles(t *testing.T) {
	f := newFixture(t)
	// main and the goal both have engine.txt; the goal changes it, and main
	// deletes it.
	f.git("checkout", "--quiet", "main")
	f.write("engine.txt", "v0\n")
	f.commitAll("feat: add the engine")
	f.on("")
	f.git("merge", "--quiet", "--no-edit", "main")
	f.git("checkout", "--quiet", "main")
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.git("checkout", "--quiet", "main")
	f.git("rm", "--quiet", "engine.txt")
	f.git("commit", "--quiet", "-m", "refactor: drop the engine")
	f.on("")
	if _, err := f.repo.Run(f.ctx, "merge", "--no-edit", "main"); err == nil {
		t.Fatal("the merge didn't conflict")
	}
	f.write("engine.txt", "ward\n")
	f.git("add", "-A")
	f.git("commit", "--quiet", "--no-edit")
	f.git("checkout", "--quiet", "main")

	// No line settles a file changed on one side and deleted on the other:
	// the rebase takes it as the reviewed resolution has it.
	res := f.build(Options{})
	if !res.Rebased || res.Squashed || len(res.Stack) != 1 {
		t.Fatalf("laid out as %+v", res)
	}
	if got := f.subjects(res.Tip()); got[len(got)-1] != "feat: add ward" {
		t.Errorf("subjects = %v", got)
	}
	if !f.sameTree(res.Tip(), f.goal.IntegrationBranch()) {
		t.Error("the tip doesn't hold the reviewed resolution")
	}
}

func TestAFailingGateGoesToAnAgent(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.git("checkout", "--quiet", "main")
	res := f.build(
		Options{Gate: "x", RunGate: func(context.Context, string, string) (gate.Result, error) {
			return gate.Result{Output: "lint: ward is unused"}, nil
		}},
	)
	failed := res.Failing()
	if failed == nil || failed.Output != "lint: ward is unused" {
		t.Fatalf("failing = %+v", failed)
	}
	// It never lands, forced or not.
	if _, err := Land(
		f.ctx,
		f.store,
		f.goal,
		res,
		Push,
		"origin",
		nil,
	); !errors.As(
		err,
		new(*GateError),
	) {
		t.Errorf("landing a failing goal = %v", err)
	}
	for range 2 {
		if err := RepairGate(f.store, f.goal, failed, time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := f.store.Tasks("set")
	var repairs []*queue.Task
	for _, task := range tasks {
		if task.Kind == queue.GateRepair {
			repairs = append(repairs, task)
		}
	}
	if len(repairs) != 1 || repairs[0].Workstream != "cards" ||
		strings.Count(repairs[0].Body, "lint: ward is unused") != 2 {
		t.Errorf("repairs = %+v", repairs)
	}
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Errorf("the goal is %s", g.State)
	}
}

func TestALayoutThatCantLandIsMadeAgain(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.git("checkout", "--quiet", "main")
	res := f.build(Options{})
	if !Current(f.ctx, f.store, f.goal, res) {
		t.Fatal("a fresh layout isn't current")
	}
	// Once the repo signs its commits, unsigned ones are laid out again.
	f.git("config", "commit.gpgSign", "true")
	if Current(f.ctx, f.store, f.goal, res) {
		t.Error("a layout of unsigned commits is current in a repo that signs")
	}
	f.git("config", "commit.gpgSign", "false")
	// A layout ending in a merge, as an older diatom made, is too.
	merge := f.git("commit-tree", res.Tip()+"^{tree}", "-p", res.Tip(), "-p", "main", "-m", "Merge")
	res.Stack[len(res.Stack)-1].Tip = merge
	if Current(f.ctx, f.store, f.goal, res) {
		t.Error("a layout with a merge is current")
	}
}
