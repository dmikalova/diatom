package gate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockPoll is how often a gate waiting its turn tries again.
const lockPoll = 200 * time.Millisecond

// lockPath is the lock every gate on the machine takes, whichever repo and
// worktree it runs in. Tools a gate runs, such as golangci-lint, allow only
// one run at a time on the machine and fail the others outright, so gates
// running side by side would fail each other for nothing.
var lockPath = filepath.Join(os.TempDir(), "diatom-gate.lock")

// Serial makes run take its turn: one gate runs at a time on the machine.
// Waiting is outside run, so a gate given a time limit with Within, inside
// Serial, doesn't count its wait as being stuck.
func Serial(run Runner) Runner {
	return func(ctx context.Context, dir, command string) (Result, error) {
		unlock, err := lock(ctx)
		if err != nil {
			return Result{}, err
		}
		defer unlock()
		return run(ctx, dir, command)
	}
}

// lock waits for the machine's gate lock, until ctx is done.
func lock(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
