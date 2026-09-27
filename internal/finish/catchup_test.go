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

func TestBuildSquashesOntoAConflictingBase(t *testing.T) {
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
	var gotSubjects []string
	res := f.build(Options{Progress: &progress, Message: func(_ context.Context, stat, _ string,
		subjects []string,
	) (string, error) {
		gotSubjects = subjects
		if !strings.Contains(stat, "engine.txt") {
			t.Errorf("stat = %q", stat)
		}
		return "feat: add ward and warden\n", nil
	}})
	if !res.Squashed || len(res.Stack) != 1 || res.Stack[0].Branch != res.Final {
		t.Fatalf("laid out as %+v", res)
	}
	tip := res.Tip()
	// One commit on main's tip, so landing keeps the history linear.
	if parents := strings.Fields(f.git("rev-list", "--parents", "-n1", tip)); len(parents) != 2 ||
		parents[1] != f.git("rev-parse", "main") {
		t.Errorf("the tip's parents = %v, want main alone", parents)
	}
	if !f.sameTree(tip, f.goal.IntegrationBranch()) {
		t.Error("the tip doesn't hold the resolution")
	}
	if got := f.git("log", "-1", "--format=%s", tip); got != "feat: add ward and warden" ||
		strings.Join(gotSubjects, ",") != "feat: add ward,feat: add warden" {
		t.Errorf("subject %q from %v", got, gotSubjects)
	}
	if !strings.Contains(progress.String(), "squashing them into one commit") {
		t.Errorf("progress:\n%s", progress.String())
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
