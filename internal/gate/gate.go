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

// Run runs command with `sh -c` in dir. A failing command is a Result that
// did not pass; the error is only for a gate that could not run at all.
func Run(ctx context.Context, dir, command string) (Result, error) {
	if strings.TrimSpace(command) == "" {
		return Result{}, errors.New("no gate is configured: set gate in .diatom/config.yaml")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	res := Result{Passed: err == nil, Output: Tail(out.String(), tailLines)}
	if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
		return res, fmt.Errorf("gate %q: %w", command, err)
	}
	return res, ctx.Err()
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
