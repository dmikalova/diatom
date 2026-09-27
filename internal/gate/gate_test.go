package gate

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	res, err := Run(ctx, dir, "echo ok")
	if err != nil || !res.Passed || res.Output != "ok" {
		t.Errorf("passing gate = %+v, %v", res, err)
	}
	res, err = Run(ctx, dir, "echo broken >&2; exit 3")
	if err != nil || res.Passed || res.Output != "broken" {
		t.Errorf("failing gate = %+v, %v", res, err)
	}
	if _, err := Run(ctx, dir, "  "); err == nil {
		t.Error("an empty gate ran")
	}
	if _, err := Run(ctx, dir+"/missing", "true"); err == nil {
		t.Error("a gate in a missing directory reported no error")
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a\nb\n", 5); got != "a\nb" {
		t.Errorf("Tail short = %q", got)
	}
	got := Tail("1\n2\n3\n4\n", 2)
	if !strings.HasPrefix(got, "[2 earlier lines cut]") || !strings.HasSuffix(got, "3\n4") {
		t.Errorf("Tail long = %q", got)
	}
}

func TestWithinStopsAStuckGate(t *testing.T) {
	start := time.Now()
	r, err := Within(
		200*time.Millisecond,
		Run,
	)(
		context.Background(),
		t.TempDir(),
		"echo testing; sleep 30",
	)
	if err != nil || r.Passed || !strings.Contains(r.Output, "testing") ||
		!strings.Contains(r.Output, "stopped after 200ms") {
		t.Errorf("stuck gate = %+v, %v", r, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the stuck gate ran for %s", time.Since(start))
	}
	if r, err := Within(
		time.Minute,
		Run,
	)(
		context.Background(),
		t.TempDir(),
		"true",
	); err != nil ||
		!r.Passed {
		t.Errorf("quick gate = %+v, %v", r, err)
	}
	// Stopped from outside, it is not stuck: the stop is the caller's.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Within(time.Minute, Run)(ctx, t.TempDir(), "sleep 30"); err == nil {
		t.Error("a stopped gate reported no error")
	}
}

func TestSerialRunsOneGateAtATime(t *testing.T) {
	lockPath = t.TempDir() + "/gate.lock"
	running, most := 0, 0
	var mu sync.Mutex
	slow := func(context.Context, string, string) (Result, error) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return Result{Passed: true}, nil
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := Serial(slow)(context.Background(), "", "x"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if most != 1 {
		t.Errorf("%d gates ran at once", most)
	}
	// A gate waiting its turn gives up when its caller does.
	unlock, err := lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Serial(slow)(ctx, "", "x"); err == nil {
		t.Error("a gate waiting for its turn ignored its caller stopping")
	}
}
