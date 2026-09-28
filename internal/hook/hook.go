// Package hook implements the two Claude Code hooks diatom gives every agent
// session (ADR 0005). PreToolUse blocks git commands that change the
// repository, because the harness does every git operation, commands that
// background or sleep to wait out the command time limit, and file tools
// reaching into diatom's state beyond the session's own. Stop runs the gate
// before the session may end and sends a failure back to the agent, which
// keeps fixing in the same session: the cheapest retry, because nothing has to
// be reloaded.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// toolInput is the part of a PreToolUse event the hook reads.
type toolInput struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
	} `json:"tool_input"`
}

// PreToolUse reads a PreToolUse event from in and writes a deny decision to
// out when it is a Bash command that changes the repository through git, or
// backgrounds or sleeps to wait out the command time limit, or a file tool
// reaching into diatom's state outside scope.
func PreToolUse(in io.Reader, out io.Writer, scope Scope) error {
	var ev toolInput
	if err := json.NewDecoder(in).Decode(&ev); err != nil {
		return fmt.Errorf("PreToolUse event: %w", err)
	}
	var reason string
	if ev.ToolName != "Bash" {
		reason = scope.deny(ev)
	} else if blocked := BlockedGit(ev.ToolInput.Command); blocked != "" {
		reason = fmt.Sprintf(
			"diatom runs every git operation that changes the repository, so `%s` is blocked. "+
				"Only edit files: when you finish, the harness runs the gate and commits your work. "+
				"Read-only git commands such as status, diff, log and show are allowed, and so are the ones "+
				"that only change files: `git restore <file>` or `git checkout -- <file>` to undo your edits, "+
				"`git show <rev>:<file> > <file>` for a file as a commit had it, and `git rm` and `git mv`.",
			blocked,
		)
	} else if waits := BlockedWait(ev.ToolInput.Command); waits != "" {
		reason = fmt.Sprintf(
			"`%s` is blocked: commands run in the foreground, and nothing may be backgrounded or "+
				"slept on to get round the command time limit. Run a narrower check that finishes in "+
				"time, such as one package's tests or one linter. diatom runs the whole gate itself when "+
				"you finish and sends any failure back to you.",
			waits,
		)
	}
	if reason == "" {
		return nil
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

// GateRunner runs a gate command in a directory.
type GateRunner = gate.Runner

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

	start := time.Now()
	res, err := runGate(ctx, spec.Worktree, spec.Gate)
	if err != nil {
		return err
	}
	if err := session.AppendEvent(dir, session.GateEvent(time.Now(), spec.Gate, time.Since(start),
		res.Passed, res.Output)); err != nil {
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

// maxReportPushes is how many times StopPlanning sends a session back to
// report before letting it end.
const maxReportPushes = 2

// StopPlanning is the Stop hook of triage and grilling sessions, which have
// no gate. It sends the agent back when a task has no report, because a
// question or plan left in a reply reaches nobody: grilling must ask or hand
// in a plan, and triage must ask or finish.
func StopPlanning(dir string, spec session.Spec, out io.Writer) error {
	report, err := session.ReadReport(dir)
	if err != nil {
		return err
	}
	asked := map[string]bool{}
	for _, q := range report.Questions {
		asked[q.Task] = true
	}
	planned := map[string]bool{}
	for _, p := range report.Plans {
		planned[p.Task] = true
	}
	var missing []string
	for _, id := range spec.Tasks {
		reported := asked[id] || report.Done[id]
		if spec.Kind == queue.Grilling {
			reported = asked[id] || planned[id]
		}
		if !reported {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	st, err := session.LoadGate(dir)
	if err != nil {
		return err
	}
	if st.Attempts >= maxReportPushes {
		return nil
	}
	st.Attempts++
	if err := session.SaveGate(dir, st); err != nil {
		return err
	}
	what := "mark it done with `diatom task done <id>` once it is sorted"
	if spec.Kind == queue.Grilling {
		what = "hand in the plan with `diatom task plan <id> < plan.yaml`"
	}
	reason := fmt.Sprintf(
		"You are ending the session without reporting on task %s. Nobody reads your replies, "+
			"so anything you wrote in one is lost. Put each question to the human with "+
			"`diatom task ask <id> \"<question>\"`, one per call with your recommended answer, or %s.",
		strings.Join(missing, ", "),
		what,
	)
	return json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": reason})
}
