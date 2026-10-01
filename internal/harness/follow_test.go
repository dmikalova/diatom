package harness

import (
	"context"
	"testing"

	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/queue"
)

// commitOn commits file on branch, from the main checkout, and returns the
// commit.
func (f *fixture) commitOn(branch, file, body, msg string) string {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.main.Run(ctx, "checkout", "--quiet", branch); err != nil {
		f.t.Fatal(err)
	}
	writeFile(f.t, f.main.Dir, file, body)
	if _, err := f.main.StageAll(ctx); err != nil {
		f.t.Fatal(err)
	}
	sha, err := f.main.Commit(ctx, msg)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.main.Run(ctx, "checkout", "--quiet", "main"); err != nil {
		f.t.Fatal(err)
	}
	return sha
}

// TestGoalsFollowTheirBase pins that what lands on a goal's base branch is
// merged into its integration branch on the next pass, and that a conflict
// goes to an agent once.
func TestGoalsFollowTheirBase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, _ := f.store.Goal("set")
	if err := f.main.CreateBranch(ctx, g.IntegrationBranch(), "main"); err != nil {
		t.Fatal(err)
	}
	f.commitOn(g.IntegrationBranch(), "shared.txt", "goal\n", "feat: the goal's change")
	// A goal that has landed is left alone.
	shipped := &queue.Goal{Name: "shipped", Title: "Shipped", State: queue.GoalDone, Base: "main"}
	if err := f.store.CreateGoal(shipped); err != nil {
		t.Fatal(err)
	}
	if err := f.main.CreateBranch(ctx, shipped.IntegrationBranch(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := finish.Save(f.store.GoalDir("shipped"), &finish.Result{
		Landing: &finish.Landing{Remote: "origin", How: finish.Push},
	}); err != nil {
		t.Fatal(err)
	}

	// main moves on elsewhere: the goal takes it in at once.
	landed := f.commitOn("main", "other.txt", "landed\n", "feat: another goal")
	if _, _, err := f.h.plan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if in, _ := f.main.IsAncestor(ctx, landed, g.IntegrationBranch()); !in {
		t.Fatal("the integration branch lacks what landed on main")
	}
	if in, _ := f.main.IsAncestor(ctx, landed, shipped.IntegrationBranch()); in {
		t.Error("a goal on its way upstream took main in")
	}

	// main moves on in the goal's own code: an agent merges it in, once.
	before, _ := f.main.RevParse(ctx, g.IntegrationBranch())
	f.commitOn("main", "shared.txt", "main\n", "feat: main's change")
	for range 2 {
		if _, _, err := f.h.plan(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := f.main.RevParse(ctx, g.IntegrationBranch()); after != before {
		t.Error("a conflicting base moved the integration branch")
	}
	tasks, _ := f.store.Tasks("set")
	var merges []*queue.Task
	for _, task := range tasks {
		if task.Kind == queue.Conflict && task.Merge != "" {
			merges = append(merges, task)
		}
	}
	if len(merges) != 1 || merges[0].Merge != "main" || merges[0].Workstream != "cards" {
		t.Errorf("catch-up tasks = %+v", merges)
	}
}
