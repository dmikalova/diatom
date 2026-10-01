package finish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
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

// conflictingBase has main change the goal's own file, and merges it into
// the integration branch with the catch-up's resolution, as a conflict task's
// agent would: merge commits it through diatom, recording the resolution.
func (f *fixture) conflictingBase(merge func()) {
	f.t.Helper()
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.task("cards", f.work("cards", "cards.txt", "warden\n", "feat: add warden"))
	f.git("checkout", "--quiet", "main")
	f.write("engine.txt", "not ward\n")
	f.commitAll("feat: something else")
	f.on("")
	merge()
	f.git("checkout", "--quiet", "main")
}

func TestBuildSquashesWhatTheBaseConflictsWith(t *testing.T) {
	f := newFixture(t)
	f.conflictingBase(func() {
		if _, err := f.repo.Run(f.ctx, "merge", "--no-edit", "main"); err == nil {
			t.Fatal("the merge didn't conflict")
		}
		f.write("engine.txt", "ward, not ward\n")
		f.git("add", "-A")
		f.git("commit", "--quiet", "--no-edit")
	})

	// Nothing recorded how the conflict was resolved, so git can't replay
	// the first commit: it and the one after are squashed into one commit of
	// the reviewed merge, with no agent.
	var progress strings.Builder
	res := f.build(Options{Progress: &progress})
	if strings.Join(res.Squashed, "|") != "feat: add ward|feat: add warden" || res.Carried ||
		len(res.Stack) != 1 {
		t.Fatalf("laid out as %+v", res)
	}
	tip := res.Tip()
	if merges := f.git("rev-list", "--merges", "main.."+tip); merges != "" {
		t.Errorf("merges on the way: %s", merges)
	}
	if got := f.subjects(tip); strings.Join(got, "|") != "feat: add ward" {
		t.Errorf("subjects = %v", got)
	}
	if !f.sameTree(tip, f.goal.IntegrationBranch()) {
		t.Error("the tip isn't the reviewed merge")
	}
	msg := f.git("log", "-1", "--format=%B", tip)
	if !strings.Contains(msg, "reviewed merge of main") ||
		!strings.Contains(msg, "feat: add warden") {
		t.Errorf("the squash's message:\n%s", msg)
	}
	if !strings.Contains(progress.String(), "squashing that commit and the ones after it") {
		t.Errorf("progress:\n%s", progress.String())
	}
}

func TestBuildReplaysTheReviewedResolution(t *testing.T) {
	f := newFixture(t)
	f.conflictingBase(func() {
		if res, err := f.repo.MergeNoCommit(f.ctx, "main"); err != nil || res != git.Conflicted {
			t.Fatalf("the merge = %v, %v", res, err)
		}
		f.write("engine.txt", "ward, not ward\n")
		if _, err := f.repo.CommitMerge(f.ctx); err != nil {
			t.Fatal(err)
		}
	})

	// The catch-up's resolution was recorded as it was committed, so git
	// settles the replay's conflict the same way, and each commit lands as
	// it was made.
	res := f.build(Options{})
	if len(res.Squashed) > 0 || res.Carried || res.Unstacked != "" {
		t.Fatalf("laid out as %+v", res)
	}
	tip := res.Tip()
	if got := f.subjects(tip); strings.Join(got, "|") != "feat: add ward|feat: add warden" {
		t.Errorf("subjects = %v", got)
	}
	if got := f.git("show", tip+"~1:engine.txt"); got != "ward, not ward" {
		t.Errorf("the replayed commit's engine.txt = %q", got)
	}
	if !f.sameTree(tip, f.goal.IntegrationBranch()) {
		t.Error("the tip isn't the reviewed merge")
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
	// So is one with conflicts an agent settled, as an older diatom made.
	res = f.build(Options{})
	if err := yaml.Unmarshal([]byte("settlements:\n  - commit: abc\n"), res); err != nil {
		t.Fatal(err)
	}
	if Current(f.ctx, f.store, f.goal, res) {
		t.Error("a layout with settlements is current")
	}
}

func TestFollowLocal(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.git("checkout", "--quiet", "main")
	tip := f.build(Options{}).Tip()
	start := f.git("rev-parse", "main")

	// Uncommitted changes to what landed keep main where it is.
	f.write("engine.txt", "mine\n")
	if note := FollowLocal(
		f.ctx,
		f.store,
		f.goal,
		"",
		tip,
	); !strings.Contains(
		note,
		"wasn't moved",
	) ||
		f.git("rev-parse", "main") != start {
		t.Errorf("over uncommitted changes to what landed: %q", note)
	}
	f.git("checkout", "--quiet", "--", ".")
	f.git("clean", "-fdq")
	// Changes elsewhere stay as they were, and main follows.
	f.write("notes.txt", "mine\n")
	if note := FollowLocal(f.ctx, f.store, f.goal, "", tip); note != "your main follows" ||
		f.git("rev-parse", "main") != tip {
		t.Errorf("with other changes: %q", note)
	}
	if b, _ := os.ReadFile(filepath.Join(f.repo.Dir, "notes.txt")); string(b) != "mine\n" {
		t.Error("the human's own change was lost")
	}
	// A main with commits of its own is left to pull.
	f.git("reset", "--quiet", "--hard", start)
	f.write("readme.txt", "hi\n")
	f.commitAll("docs: a readme")
	if note := FollowLocal(
		f.ctx,
		f.store,
		f.goal,
		"",
		tip,
	); !strings.Contains(
		note,
		"commits of its own",
	) {
		t.Errorf("with commits of its own: %q", note)
	}
}
