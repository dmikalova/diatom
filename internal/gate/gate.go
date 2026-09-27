// Package gate runs a repo's gate: the check command every commit must pass
// before it lands (ADR 0005). It is never skipped.
package gate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Result is one run of the gate.
type Result struct {
	Passed bool
	// Output is the command's combined stdout and stderr, trimmed to its
	// last lines so it fits in a prompt.
	Output string
}

// tailLines is how much of the gate's output reaches the agent. The end of
// the output is where test runners and linters put their summary.
const tailLines = 200

// stopWait bounds how long a finished or stopped gate's leftovers may hold
// its output open.
const stopWait = 5 * time.Second

// Run runs command with `sh -c` in dir. A failing command is a Result that
// did not pass; the error is only for a gate that could not run at all.
func Run(ctx context.Context, dir, command string) (Result, error) {
	if strings.TrimSpace(command) == "" {
		return Result{}, errors.New("no gate is configured: set gate in .diatom/config.toml")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	// Its own process group, so stopping the gate stops everything it
	// started, and nothing it left behind holds its output open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = stopWait
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	res := Result{Passed: err == nil, Output: Tail(out.String(), tailLines)}
	if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
		return res, fmt.Errorf("gate %q: %w", command, err)
	}
	return res, ctx.Err()
}

// Runner runs a gate command in a directory.
type Runner func(ctx context.Context, dir, command string) (Result, error)

// Within makes run give up after d: a gate still running then is stuck, and
// fails with a note saying so. A d of 0 never gives up.
func Within(d time.Duration, run Runner) Runner {
	if d <= 0 {
		return run
	}
	return func(ctx context.Context, dir, command string) (Result, error) {
		limited, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		r, err := run(limited, dir, command)
		if ctx.Err() == nil && errors.Is(limited.Err(), context.DeadlineExceeded) {
			return Result{Output: strings.TrimSpace(r.Output + "\n\n" + fmt.Sprintf(
				"The gate was stopped after %s. A good run takes far less, so something in it is "+
					"stuck, such as a test waiting forever: find what hangs rather than running it again.",
				d))}, nil
		}
		return r, err
	}
}

// Tail returns the last n lines of s, noting how many were cut.
func Tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	cut := len(lines) - n
	return fmt.Sprintf("[%d earlier lines cut]\n%s", cut, strings.Join(lines[cut:], "\n"))
}
