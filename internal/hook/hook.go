// Package hook implements the two Claude Code hooks diatom gives every agent
// session (ADR 0005). PreToolUse blocks git commands that change the
// repository, because the harness does every git operation. Stop runs the gate
// before the session may end and sends a failure back to the agent, which
// keeps fixing in the same session: the cheapest retry, because nothing has to
// be reloaded.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/session"
)

// toolInput is the part of a PreToolUse event the hook reads.
type toolInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// PreToolUse reads a PreToolUse event from in and, when it is a Bash command
// that changes the repository through git, writes a deny decision to out.
func PreToolUse(in io.Reader, out io.Writer) error {
	var ev toolInput
	if err := json.NewDecoder(in).Decode(&ev); err != nil {
		return fmt.Errorf("PreToolUse event: %w", err)
	}
	if ev.ToolName != "Bash" {
		return nil
	}
	blocked := BlockedGit(ev.ToolInput.Command)
	if blocked == "" {
		return nil
	}
	reason := fmt.Sprintf(
		"diatom runs every git operation that changes the repository, so `%s` is blocked. "+
			"Only edit files: when you finish, the harness runs the gate and commits your work. "+
			"Read-only git commands such as status, diff, log and show are allowed.",
		blocked,
	)
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

// GateRunner runs a gate command in a directory.
type GateRunner func(ctx context.Context, dir, command string) (gate.Result, error)

// Stop runs the gate for the session in dir and writes a block decision to
// out when the agent has to keep fixing. It lets the session end when:
//
//   - the files are unchanged since the last commit, or since the gate last
//     passed on them
//   - the gate passes
//   - the gate has failed GateAttempts times; the harness then retries the
//     tasks with more effort
func Stop(
	ctx context.Context,
	dir string,
	spec session.Spec,
	runGate GateRunner,
	out io.Writer,
) error {
	st, err := session.LoadGate(dir)
	if err != nil || st.Exhausted {
		return err
	}
	repo := git.Repo{Dir: spec.Worktree}

	markers, err := repo.ConflictMarkers(ctx)
	if err != nil {
		return err
	}
	if len(markers) > 0 {
		return fail(dir, spec, st, out, "Conflict markers are still in these files; resolve them:",
			fmt.Sprint(markers))
	}

	fp, err := repo.Fingerprint(ctx)
	if err != nil {
		return err
	}
	head, err := repo.HeadTree(ctx)
	if err != nil {
		return err
	}
	if fp == st.Passed || (fp == head && !repo.MergeInProgress(ctx)) {
		return nil
	}

	res, err := runGate(ctx, spec.Worktree, spec.Gate)
	if err != nil {
		return err
	}
	if !res.Passed {
		return fail(
			dir,
			spec,
			st,
			out,
			fmt.Sprintf("The gate `%s` failed. Fix the cause, then finish again:", spec.Gate),
			res.Output,
		)
	}
	// The gate's formatters may have rewritten files, so the fingerprint
	// that passed is the one after the run.
	if st.Passed, err = repo.Fingerprint(ctx); err != nil {
		return err
	}
	return session.SaveGate(dir, st)
}

// fail records a failed attempt and blocks the session from ending, unless
// that was the last attempt.
func fail(
	dir string,
	spec session.Spec,
	st session.GateState,
	out io.Writer,
	reason, output string,
) error {
	st.Attempts++
	st.Output = output
	st.Exhausted = st.Attempts >= spec.GateAttempts
	if err := session.SaveGate(dir, st); err != nil {
		return err
	}
	if st.Exhausted {
		return nil
	}
	msg := fmt.Sprintf(
		"%s\n\n%s\n\n(attempt %d of %d)",
		reason,
		output,
		st.Attempts,
		spec.GateAttempts,
	)
	return json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": msg})
}
