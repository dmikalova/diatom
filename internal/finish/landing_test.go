package finish

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// withRemote gives the fixture an origin on GitHub, as far as its URL says,
// that is really a bare repo holding main.
func (f *fixture) withRemote() git.Repo {
	f.t.Helper()
	bare := git.Repo{Dir: f.t.TempDir()}
	if _, err := bare.Run(f.ctx, "init", "--bare", "--initial-branch=main"); err != nil {
		f.t.Fatal(err)
	}
	f.git("config", "url."+bare.Dir+".insteadOf", "https://github.com/me/toy")
	f.git("remote", "add", "origin", "https://github.com/me/toy")
	f.git("push", "--quiet", "origin", "main")
	return bare
}

// fakeGH answers gh with the pull request's state and the check runs on
// main upstream, and counts the calls.
type fakeGH struct {
	prState, runs string
	calls         int
}

func (g *fakeGH) run(_ context.Context, _ string, args ...string) (string, error) {
	g.calls++
	switch {
	case args[0] == "api":
		return g.runs, nil
	case args[1] == "view" && args[4] == "url": // looking for an open one by branch
		return "", context.Canceled
	case args[1] == "view":
		return `{"state":"` + g.prState + `","statusCheckRollup":[` +
			`{"name":"ci","status":"COMPLETED","conclusion":"SUCCESS"},{"context":"lint","state":"PENDING"}]}`, nil
	case args[1] == "create":
		return "https://github.com/me/toy/pull/7", nil
	}
	return "", nil
}

func TestWatchFinishesOnceMergedAndPassing(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.withRemote()
	res := f.build(Options{})
	gh := &fakeGH{prState: "OPEN"}
	if _, err := Land(f.ctx, f.store, f.goal, res, PRs, "origin", false, gh.run); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	watch := func(at time.Time) bool {
		t.Helper()
		finished, err := Watch(f.ctx, f.store, f.goal, gh.run, at)
		if err != nil {
			t.Fatal(err)
		}
		return finished
	}
	landing := func() *Landing {
		t.Helper()
		got, err := Load(f.store.GoalDir("set"))
		if err != nil {
			t.Fatal(err)
		}
		return got.Landing
	}

	if watch(now) {
		t.Fatal("finished before merging")
	}
	l := landing()
	if l.Merged != "" || len(l.PRs) != 1 || l.PRs[0].State != "OPEN" ||
		l.PRs[0].Checks != ChecksPending {
		t.Errorf("landing while open = %+v", l)
	}
	if got := Summary(
		f.goal,
		&Result{Stack: res.Stack, Landing: l},
	); got != "pull requests #7 open …" {
		t.Errorf("summary = %q", got)
	}
	calls := gh.calls
	if watch(now.Add(time.Minute)) || gh.calls != calls {
		t.Error("watched again before WatchEvery")
	}

	// Squash-merged upstream: another commit with the goal's files.
	squash := f.git("commit-tree", res.Tip()+"^{tree}", "-p", "main", "-m", "New set (#7)")
	f.git("push", "--quiet", "origin", squash+":refs/heads/main")
	gh.prState, gh.runs = "MERGED", "ci\tin_progress\t"
	now = now.Add(WatchEvery)
	if watch(now) {
		t.Fatal("finished with the checks still running")
	}
	if l := landing(); l.Merged != squash || l.Checks != ChecksPending ||
		!strings.Contains(
			Summary(f.goal, &Result{Landing: l}),
			"merged into origin/main · checks running",
		) {
		t.Errorf("landing once merged = %+v", l)
	}

	gh.runs = "ci\tcompleted\tsuccess\nrelease\tcompleted\tfailure"
	now = now.Add(WatchEvery)
	if watch(now) {
		t.Fatal("finished with a check failing")
	}
	if l := landing(); l.Checks != ChecksFailed || strings.Join(l.Failing, ",") != "release" {
		t.Errorf("landing with a failing check = %+v", l)
	}

	gh.runs = "ci\tcompleted\tsuccess\nrelease\tcompleted\tskipped"
	now = now.Add(WatchEvery)
	if !watch(now) {
		t.Fatal("not finished with the checks passing")
	}
	if g, _ := f.store.Goal("set"); g.State != queue.GoalFinished {
		t.Errorf("goal state = %s", g.State)
	}
}

func TestWatchWithoutChecks(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	f.withRemote()
	res := f.build(Options{})
	gh := &fakeGH{}
	if _, err := Land(f.ctx, f.store, f.goal, res, Push, "origin", false, gh.run); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	if finished, err := Watch(f.ctx, f.store, f.goal, gh.run, now); err != nil || finished {
		t.Fatalf("Watch right after the push = %v, %v", finished, err)
	}
	// A repo without CI finishes once nothing has shown up for a while.
	if finished, err := Watch(f.ctx, f.store, f.goal, gh.run, now.Add(NoChecksGrace)); err != nil ||
		!finished {
		t.Errorf("Watch after the grace = %v, %v", finished, err)
	}
}

func TestWatchWithoutRemote(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	res := f.build(Options{})
	if finished, err := Watch(
		f.ctx,
		f.store,
		f.goal,
		(&fakeGH{}).run,
		time.Now(),
	); finished ||
		err != nil {
		t.Errorf("Watch of a goal not landed, without a remote = %v, %v", finished, err)
	}
	// Once landing was asked for, a missing remote is a problem.
	res.Landing = &Landing{Remote: "origin", How: Push}
	if err := Save(f.store.GoalDir("set"), res); err != nil {
		t.Fatal(err)
	}
	finished, err := Watch(f.ctx, f.store, f.goal, (&fakeGH{}).run, time.Now())
	if finished || err == nil || !strings.Contains(err.Error(), "no remote origin") {
		t.Errorf("Watch without a remote = %v, %v", finished, err)
	}
	res, _ = Load(f.store.GoalDir("set"))
	if !strings.Contains(Summary(f.goal, res), "last check failed: there is no remote origin") {
		t.Errorf("summary = %q", Summary(f.goal, res))
	}
}

func TestWatchOffGitHubHasNoChecks(t *testing.T) {
	f := newFixture(t)
	f.task("engine", f.work("engine", "engine.txt", "ward\n", "feat: add ward"))
	bare := git.Repo{Dir: t.TempDir()}
	if _, err := bare.Run(f.ctx, "init", "--bare", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}
	f.git("remote", "add", "origin", bare.Dir)
	res := f.build(Options{})
	gh := &fakeGH{runs: "ci\tin_progress\t"}
	if _, err := Land(f.ctx, f.store, f.goal, res, Push, "origin", false, gh.run); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	if _, err := Watch(f.ctx, f.store, f.goal, gh.run, now); err != nil || gh.calls != 0 {
		t.Fatalf("Watch = %v with %d gh calls", err, gh.calls)
	}
	if finished, _ := Watch(f.ctx, f.store, f.goal, gh.run, now.Add(NoChecksGrace)); !finished {
		t.Error("a goal merged off GitHub never finished")
	}
}
