package finish

import (
	"strings"
	"testing"
	"time"

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
