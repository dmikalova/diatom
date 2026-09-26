package harness

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

func TestSchedulerFinishesALandedGoal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.add("engine", "Add ward")
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward\n")
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	f.step()
	g, _ := f.store.Goal("set")
	if err := finish.MarkDone(ctx, f.store, g, true); err != nil {
		t.Fatal(err)
	}
	res, err := finish.Build(ctx, f.store, g, finish.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bare := git.Repo{Dir: t.TempDir()}
	for _, run := range []func() error{
		func() error { _, err := bare.Run(ctx, "init", "--bare", "--initial-branch=main"); return err },
		func() error {
			_, err := f.main.Run(ctx, "config", "url."+bare.Dir+".insteadOf", "https://github.com/me/toy")
			return err
		},
		func() error {
			_, err := f.main.Run(ctx, "remote", "add", "origin", "https://github.com/me/toy")
			return err
		},
	} {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	checks := ""
	f.h.GH = func(context.Context, string, ...string) (string, error) {
		return checks, nil
	}
	if _, err := finish.Land(
		ctx,
		f.store,
		g,
		res,
		finish.Push,
		"origin",
		false,
		f.h.GH,
	); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	f.h.Now = func() time.Time { return now }
	checks = "ci\tqueued\t"
	f.step()
	if g, _ := f.store.Goal("set"); g.State != queue.GoalDone {
		t.Fatalf("goal = %s with the checks queued", g.State)
	}
	checks = "ci\tcompleted\tsuccess"
	now = now.Add(finish.WatchEvery)
	f.step()
	if g, _ := f.store.Goal("set"); g.State != queue.GoalFinished {
		t.Fatalf("goal = %s once merged and passing", g.State)
	}
	if _, err := os.Stat(f.store.WorktreeDir("set", "engine")); err == nil {
		t.Error("the finished goal's worktree is still there")
	}
}
