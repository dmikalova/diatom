package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// inRepo makes a git repo that ignores .diatom/, with an isolated registry,
// and changes into it.
func inRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	r := git.Repo{Dir: dir}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"commit", "--allow-empty", "-m", "chore: start"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, ".git", "info", "exclude"),
		[]byte(".diatom/\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Chdir(dir)
	return dir
}

func diatom(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestGoalAndTaskCommands(t *testing.T) {
	repo := inRepo(t)
	if code, _, stderr := diatom(
		t,
		"",
		"goal",
		"new",
		"set",
		"-title",
		"New set",
		"-ws",
		"engine,cards:engine",
		"-active",
	); code != 0 {
		t.Fatalf("goal new: %s", stderr)
	}
	g, err := queue.Open(repo).Goal("set")
	if err != nil {
		t.Fatal(err)
	}
	if g.State != queue.GoalActive || g.Base != "main" || len(g.Workstreams) != 2 ||
		g.Workstreams[1].DependsOn[0] != "engine" {
		t.Errorf("goal = %+v", g)
	}

	code, stdout, stderr := diatom(
		t,
		"Ward stops the next damage.\n",
		"task",
		"add",
		"-goal",
		"set",
		"-ws",
		"engine",
		"Add",
		"ward",
	)
	if code != 0 || strings.TrimSpace(stdout) != "0001" {
		t.Fatalf("task add = %d %q %q", code, stdout, stderr)
	}
	task, _ := queue.Open(repo).Task("set", "0001")
	if task.Title != "Add ward" || task.Body != "Ward stops the next damage.\n" ||
		task.Profile != "implementation" {
		t.Errorf("task = %+v", task)
	}
	if code, _, _ := diatom(t, "", "task", "add", "-goal", "set", "-ws", "web", "x"); code != 1 {
		t.Error("task add accepted an unknown workstream")
	}

	for _, sub := range []string{"pin", "park"} {
		if code, _, stderr := diatom(t, "", "goal", sub, "set"); code != 0 {
			t.Fatalf("goal %s: %s", sub, stderr)
		}
	}
	_, stdout, _ = diatom(t, "", "goal", "list")
	if !strings.Contains(stdout, "set") || !strings.Contains(stdout, "parked") ||
		!strings.Contains(stdout, "pinned") {
		t.Errorf("goal list = %q", stdout)
	}
	_, stdout, _ = diatom(t, "", "status")
	if !strings.Contains(stdout, "pending 1") {
		t.Errorf("status = %q", stdout)
	}
}

func TestQuestionsAndAnswer(t *testing.T) {
	repo := inRepo(t)
	diatom(t, "", "goal", "new", "set", "-ws", "engine")
	s := queue.Open(repo)
	if err := s.AddQuestion(
		"set",
		&queue.Question{Task: "0001", Text: "Does ward stack?"},
	); err != nil {
		t.Fatal(err)
	}
	_, stdout, _ := diatom(t, "", "questions")
	if !strings.Contains(stdout, "set 0001 (task 0001)") ||
		!strings.Contains(stdout, "    Does ward stack?") {
		t.Errorf("questions = %q", stdout)
	}
	if code, _, stderr := diatom(t, "", "answer", "set", "0001", "No,", "never."); code != 0 {
		t.Fatal(stderr)
	}
	qs, _ := s.Questions("set", queue.QuestionOpen)
	if qs[0].Answer != "No, never." {
		t.Errorf("answer = %q", qs[0].Answer)
	}
	if _, stdout, _ = diatom(t, "", "questions"); stdout != "" {
		t.Errorf("answered question still listed: %q", stdout)
	}
}

func TestGoalNewRequiresIgnoredState(t *testing.T) {
	repo := inRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The developer's own global excludes would still ignore .diatom/.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	code, _, stderr := diatom(t, "", "goal", "new", "set")
	if code != 1 || !strings.Contains(stderr, "not ignored") {
		t.Errorf("goal new = %d %q", code, stderr)
	}
}

func TestTaskTool(t *testing.T) {
	dir := t.TempDir()
	spec := session.Spec{ID: "s", Tasks: []string{"0001"}}
	if err := session.Create(dir, spec); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.EnvVar, dir)
	for _, args := range [][]string{
		{"task", "note", "0001", "found", "it"},
		{"task", "ask", "0001", "Which?"},
		{"task", "done", "0001"},
	} {
		if code, _, stderr := diatom(t, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}
	r, _ := session.ReadReport(dir)
	if !r.Done["0001"] || r.Notes[0].Text != "found it" || r.Questions[0].Text != "Which?" {
		t.Errorf("report = %+v", r)
	}
	if code, _, _ := diatom(t, "", "task", "note", "0001"); code != 2 {
		t.Error("task note without text was accepted")
	}
	if code, _, _ := diatom(t, "", "task", "done", "0002"); code != 1 {
		t.Error("task done of a foreign task was accepted")
	}
}

func TestHookCommands(t *testing.T) {
	code, stdout, _ := diatom(
		t,
		`{"tool_name":"Bash","tool_input":{"command":"git push"}}`,
		"hook",
		"pre-tool-use",
	)
	if code != 0 || !strings.Contains(stdout, `"deny"`) {
		t.Errorf("pre-tool-use = %d %q", code, stdout)
	}
	t.Setenv(session.EnvVar, "")
	if code, _, _ := diatom(t, "{}", "hook", "stop"); code != 1 {
		t.Error("stop outside a session succeeded")
	}
	if code, _, _ := diatom(t, "", "hook", "nope"); code != 2 {
		t.Error("an unknown hook was accepted")
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := diatom(t, ""); code != 2 {
		t.Error("no command did not print usage")
	}
	if code, _, stderr := diatom(
		t,
		"",
		"frobnicate",
	); code != 2 ||
		!strings.Contains(stderr, "Usage:") {
		t.Errorf("unknown command = %d %q", code, stderr)
	}
	if code, stdout, _ := diatom(t, "", "version"); code != 0 || stdout != "dev\n" {
		t.Errorf("version = %q", stdout)
	}
}
