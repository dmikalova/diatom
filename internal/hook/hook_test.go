package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/session"
)

func TestBlockedGit(t *testing.T) {
	allowed := []string{
		"git status",
		"git diff HEAD~1 -- engine/",
		"git log --oneline -5 && go test ./...",
		"git -C ../other --no-pager log",
		"git branch",
		"git branch -a",
		"git branch --contains abc123",
		"git stash list",
		"git config --get user.name",
		"git restore engine/ward.go",
		"git tag -l 'v*'",
		"git --version",
		"git",
		"ls | grep git",
		"echo 'digit commit'",
		"mage check",
		"GIT_PAGER=cat git show HEAD",
		`diatom task note 0001 "git commit is blocked, as expected"`,
		`echo 'git reset --hard'`,
		`git log --grep "git commit"`,
		`grep -rn "git push" .`,
		`find . -name '*.go' -exec gofmt -l {} +`,
	}
	for _, c := range allowed {
		if got := BlockedGit(c); got != "" {
			t.Errorf("BlockedGit(%q) = %q, want allowed", c, got)
		}
	}
	blocked := []string{
		"git commit -m wip",
		"go test ./... && git commit -am done",
		"git checkout main",
		"git -C . merge other",
		"git reset --hard",
		"git push",
		"git stash",
		"git branch -D old",
		"git branch new-branch",
		"git tag v1",
		"git restore --staged x",
		"git add .",
		"git clean -fd",
		"sh -c \"git commit -m x\"",
		"bash -c 'cd x; git rebase main'",
		"echo $(git commit)",
		"sudo git reset",
		"env A=1 git commit",
		"/usr/bin/git merge x",
		"git -c core.hooksPath=/dev/null commit",
		"git worktree add ../x",
		"git config user.name bot",
		"git my-alias",
		"echo x |\n git am",
		`bash -lc 'git rebase main'`,
		`zsh -c "git stash"`,
		`eval "git commit -m x"`,
		`echo "$(git commit -m x)"`,
		"echo \"`git push`\"",
		`find . -exec git reset {} \;`,
	}
	for _, c := range blocked {
		if got := BlockedGit(c); got == "" {
			t.Errorf("BlockedGit(%q) allowed it", c)
		}
	}
}

func TestSplit(t *testing.T) {
	got := split(`a "b c" 'd;e' f\ g; h | i && $(j k) ` + "`l`")
	want := [][]string{{"a", "b c", "d;e", "f g"}, {"h"}, {"i"}, {"j", "k"}, {"l"}}
	if len(got) != len(want) {
		t.Fatalf("split = %q", got)
	}
	for i := range want {
		if strings.Join(got[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("split[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPreToolUse(t *testing.T) {
	var out bytes.Buffer
	if err := PreToolUse(
		strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`),
		&out,
	); err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if got.HookSpecificOutput.PermissionDecision != "deny" ||
		!strings.Contains(got.HookSpecificOutput.PermissionDecisionReason, "git commit -m x") {
		t.Errorf("decision = %+v", got)
	}

	for _, ev := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"git status"}}`,
		`{"tool_name":"Edit","tool_input":{"file_path":"x"}}`,
	} {
		out.Reset()
		if err := PreToolUse(strings.NewReader(ev), &out); err != nil || out.Len() != 0 {
			t.Errorf("PreToolUse(%s) wrote %q, %v", ev, out.String(), err)
		}
	}
	if err := PreToolUse(strings.NewReader("{"), &out); err == nil {
		t.Error("PreToolUse accepted broken JSON")
	}
}

// stopFixture is a worktree with one commit and a session over it.
func stopFixture(t *testing.T) (dir string, spec session.Spec, repo git.Repo) {
	t.Helper()
	ctx := context.Background()
	repo = git.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
	} {
		if _, err := repo.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	edit(t, repo, "a.txt", "one\n")
	if _, err := repo.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "chore: start"); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	spec = session.Spec{ID: "s", Worktree: repo.Dir, Gate: "gate", GateAttempts: 2}
	if err := session.Create(dir, spec); err != nil {
		t.Fatal(err)
	}
	return dir, spec, repo
}

func edit(t *testing.T, r git.Repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// countingGate passes when the file ok exists in the worktree.
func countingGate(runs *int) GateRunner {
	return func(_ context.Context, dir, _ string) (gate.Result, error) {
		*runs++
		if _, err := os.Stat(filepath.Join(dir, "ok")); err == nil {
			return gate.Result{Passed: true}, nil
		}
		return gate.Result{Output: "FAIL: ok is missing"}, nil
	}
}

func TestStopSkipsUnchangedTree(t *testing.T) {
	dir, spec, _ := stopFixture(t)
	runs := 0
	var out bytes.Buffer
	if err := Stop(context.Background(), dir, spec, countingGate(&runs), &out); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || out.Len() != 0 {
		t.Errorf("Stop on an unchanged tree ran the gate %d times and wrote %q", runs, out.String())
	}
}

func TestStopBlocksThenGivesUp(t *testing.T) {
	ctx := context.Background()
	dir, spec, repo := stopFixture(t)
	edit(t, repo, "a.txt", "two\n")
	runs := 0
	var out bytes.Buffer
	if err := Stop(ctx, dir, spec, countingGate(&runs), &out); err != nil {
		t.Fatal(err)
	}
	var dec struct{ Decision, Reason string }
	if err := json.Unmarshal(out.Bytes(), &dec); err != nil || dec.Decision != "block" ||
		!strings.Contains(
			dec.Reason,
			"ok is missing",
		) || !strings.Contains(dec.Reason, "attempt 1 of 2") {
		t.Fatalf("first failure = %q, %v", out.String(), err)
	}

	out.Reset()
	if err := Stop(ctx, dir, spec, countingGate(&runs), &out); err != nil {
		t.Fatal(err)
	}
	st, _ := session.LoadGate(dir)
	if out.Len() != 0 || !st.Exhausted || st.Attempts != 2 {
		t.Errorf("last attempt wrote %q with state %+v, want the session let go", out.String(), st)
	}

	out.Reset()
	if err := Stop(ctx, dir, spec, countingGate(&runs), &out); err != nil || runs != 2 {
		t.Errorf("Stop after exhaustion ran the gate again (%d runs, %v)", runs, err)
	}
}

func TestStopPassesAndRemembers(t *testing.T) {
	ctx := context.Background()
	dir, spec, repo := stopFixture(t)
	edit(t, repo, "ok", "")
	runs := 0
	var out bytes.Buffer
	for range 2 {
		if err := Stop(ctx, dir, spec, countingGate(&runs), &out); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := session.LoadGate(dir)
	if out.Len() != 0 || runs != 1 || st.Passed == "" {
		t.Errorf("passing gate: %d runs, output %q, state %+v", runs, out.String(), st)
	}
}

func TestStopConflictMarkers(t *testing.T) {
	dir, spec, repo := stopFixture(t)
	edit(t, repo, "a.txt", "<<<<<<< HEAD\none\n=======\ntwo\n>>>>>>> other\n")
	runs := 0
	var out bytes.Buffer
	if err := Stop(context.Background(), dir, spec, countingGate(&runs), &out); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || !bytes.Contains(out.Bytes(), []byte("Conflict markers")) {
		t.Errorf("markers: %d runs, output %q", runs, out.String())
	}
}
