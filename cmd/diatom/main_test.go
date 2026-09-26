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
	diatom(t, "", "goal", "new", "set", "-ws", "engine", "-active")
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

func TestReviewListAndGoalDone(t *testing.T) {
	repo := inRepo(t)
	ctx := context.Background()
	diatom(t, "", "goal", "new", "set", "-ws", "engine", "-active")
	r := git.Repo{Dir: repo}
	if err := os.WriteFile(
		filepath.Join(repo, "ward.go"),
		[]byte("package ward\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "feat: ward")
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Open(repo).AddTask("set", &queue.Task{Title: "Add ward", Kind: queue.Planned,
		Workstream: "engine", Commits: []string{sha}}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := diatom(t, "", "review", "-list")
	if code != 0 || !strings.Contains(stdout, sha[:7]+" ward.go#1 feat: ward (unreviewed)") {
		t.Fatalf("review -list = %d %q %q", code, stdout, stderr)
	}
	if code, _, stderr := diatom(
		t,
		"",
		"goal",
		"done",
		"set",
	); code != 1 ||
		!strings.Contains(stderr, "1 unreviewed") {
		t.Errorf("goal done with an unreviewed hunk = %d %q", code, stderr)
	}
	if code, stdout, _ := diatom(
		t,
		"",
		"goal",
		"done",
		"set",
		"-force",
	); code != 0 ||
		!strings.Contains(stdout, "done") {
		t.Errorf("goal done -force = %d %q", code, stdout)
	}
	if code, _, stderr := diatom(
		t,
		"",
		"review",
		"-list",
	); code != 1 ||
		!strings.Contains(stderr, "no open goal") {
		t.Errorf("review with every goal done = %d %q", code, stderr)
	}
}

func TestGoalFinish(t *testing.T) {
	repo := inRepo(t)
	ctx := context.Background()
	diatom(t, "", "goal", "new", "set", "-ws", "engine", "-active")
	r := git.Repo{Dir: repo}
	if _, err := r.Run(ctx, "branch", "diatom/set/integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "checkout", "--quiet", "-b", "diatom/set/ws/engine"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(repo, "ward.go"),
		[]byte("package ward\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, "feat: ward"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "checkout", "--quiet", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MergeInto(ctx, "diatom/set/integration", "diatom/set/ws/engine"); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := diatom(t, "", "goal", "finish", "set"); code != 1 ||
		!strings.Contains(stderr, "goal done set") {
		t.Errorf("goal finish on an active goal = %d %q", code, stderr)
	}
	code, stdout, stderr := diatom(t, "", "goal", "done", "set", "-force")
	if code != 0 || !strings.Contains(stdout, "laid out on diatom/set/final: 1 commits on main") {
		t.Fatalf("goal done = %d %q %q", code, stdout, stderr)
	}
	// The layout is still current, so it isn't made again.
	code, stdout, _ = diatom(t, "", "goal", "finish", "set")
	if code != 0 || strings.Contains(stdout, "laying") || !strings.Contains(stdout, "-push") {
		t.Errorf("goal finish = %d %q", code, stdout)
	}
	if code, _, _ := diatom(t, "", "goal", "finish", "set", "-push", "-prs"); code != 2 {
		t.Error("-push with -prs was accepted")
	}
	if code, _, stderr := diatom(
		t,
		"",
		"goal",
		"finish",
		"set",
		"-push",
		"-remote",
		"nowhere",
	); code != 1 ||
		!strings.Contains(stderr, "nowhere") {
		t.Errorf("push to a missing remote = %d %q", code, stderr)
	}
}

func TestReviewGoalChoice(t *testing.T) {
	inRepo(t)
	diatom(t, "", "goal", "new", "one", "-ws", "a", "-active")
	diatom(t, "", "goal", "new", "two", "-ws", "a", "-active")
	if code, _, stderr := diatom(
		t,
		"",
		"review",
		"-list",
	); code != 2 ||
		!strings.Contains(stderr, "one, two") {
		t.Errorf("review with two goals = %d %q", code, stderr)
	}
	if code, _, _ := diatom(t, "", "review", "-list", "-goal", "two"); code != 0 {
		t.Error("review -goal two failed")
	}
	if code, _, _ := diatom(t, "", "review", "-list", "-goal", "nope"); code != 1 {
		t.Error("review of a missing goal succeeded")
	}
}

func TestWorkspaceLayout(t *testing.T) {
	layout := workspaceLayout(`/opt/my "tools"/diatom`)
	for _, want := range []string{
		`command="/opt/my \"tools\"/diatom"`,
		`args "review" "-focus"`, `args "pane" "status"`, `args "pane" "questions"`, `args "pane" "intake"`,
		`tab name="scheduler"`, `args "run"`,
	} {
		if !strings.Contains(layout, want) {
			t.Errorf("layout lacks %s:\n%s", want, layout)
		}
	}
	if strings.Count(layout, "{") != strings.Count(layout, "}") {
		t.Error("layout braces don't balance")
	}
}

func TestWorkspaceRefusesInsideZellij(t *testing.T) {
	t.Setenv("ZELLIJ", "0")
	if code, _, stderr := diatom(
		t,
		"",
		"workspace",
	); code != 1 ||
		!strings.Contains(stderr, "inside zellij") {
		t.Errorf("workspace inside zellij = %d %q", code, stderr)
	}
	if code, _, _ := diatom(t, "", "pane", "nope"); code != 2 {
		t.Error("an unknown pane was accepted")
	}
}

func TestGoalGrillingCommands(t *testing.T) {
	repo := inRepo(t)
	code, stdout, stderr := diatom(
		t,
		"Implement the Grim Reminders set.\n",
		"goal",
		"new",
		"grim",
		"-title",
		"Grim Reminders",
	)
	if code != 0 || !strings.Contains(stdout, "in planning") {
		t.Fatalf("goal new = %d %q %q", code, stdout, stderr)
	}
	s := queue.Open(repo)
	tasks, _ := s.Tasks("grim")
	if len(tasks) != 1 || tasks[0].Kind != queue.Grilling ||
		tasks[0].Body != "Implement the Grim Reminders set.\n" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if code, _, stderr := diatom(
		t,
		"",
		"goal",
		"new",
		"x",
		"-ws",
		"a",
	); code != 2 ||
		!strings.Contains(stderr, "needs -active") {
		t.Errorf("planning goal with -ws = %d %q", code, stderr)
	}
	if code, _, stderr := diatom(
		t,
		"",
		"goal",
		"plan",
		"grim",
	); code != 1 ||
		!strings.Contains(stderr, "no plan yet") {
		t.Errorf("goal plan without one = %d %q", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(s.GoalDir("grim"), "plan.yaml"), []byte(
		"summary: Do it.\nworkstreams: [{name: engine}]\ntasks: [{key: a, title: Add ward, workstream: engine}]\n",
	),
		0o644); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := diatom(
		t,
		"",
		"goal",
		"plan",
		"grim",
	); code != 0 ||
		!strings.Contains(stdout, "[engine] Add ward") {
		t.Errorf("goal plan = %d %q", code, stdout)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code, stdout, stderr := diatom(
		t,
		"",
		"goal",
		"approve",
		"grim",
	); code != 0 ||
		!strings.Contains(stdout, "signed off") {
		t.Fatalf("goal approve = %d %q %q", code, stdout, stderr)
	}
	if g, _ := s.Goal("grim"); g.State != queue.GoalActive {
		t.Errorf("goal after approve = %s", g.State)
	}
}

func TestPlanningToolCommands(t *testing.T) {
	dir := t.TempDir()
	triage := session.Spec{ID: "s", Kind: queue.Triage, Tasks: []string{"0001"}}
	if err := session.Create(dir, triage); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.EnvVar, dir)
	if code, _, stderr := diatom(
		t,
		"Ward stops 1.",
		"task",
		"add-task",
		"0001",
		"-ws",
		"engine",
		"-title",
		"Weaken ward",
		"-after",
		"0003, 0004",
	); code != 0 {
		t.Fatalf("add-task: %s", stderr)
	}
	if code, _, stderr := diatom(t, "", "task", "new-goal", "0001", "-title", "Web UI"); code != 0 {
		t.Fatalf("new-goal: %s", stderr)
	}
	if code, _, _ := diatom(t, "", "task", "add-task", "0001", "-title", "x"); code != 2 {
		t.Error("add-task without -ws was accepted")
	}
	if code, _, stderr := diatom(
		t,
		"summary: x\n",
		"task",
		"plan",
		"0001",
	); code != 1 ||
		!strings.Contains(stderr, "only for grilling") {
		t.Errorf("plan in a triage session = %d %q", code, stderr)
	}
	r, _ := session.ReadReport(dir)
	if len(r.Adds) != 1 || r.Adds[0].Title != "Weaken ward" ||
		strings.Join(r.Adds[0].After, ",") != "0003,0004" ||
		r.Adds[0].Text != "Ward stops 1." ||
		len(r.Goals) != 1 {
		t.Errorf("report = %+v", r)
	}

	grill := session.Spec{ID: "g", Kind: queue.Grilling, Tasks: []string{"0002"}}
	gdir := t.TempDir()
	if err := session.Create(gdir, grill); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.EnvVar, gdir)
	if code, _, stderr := diatom(
		t,
		"summary: x\n",
		"task",
		"plan",
		"0002",
	); code != 1 ||
		!strings.Contains(stderr, "not accepted") {
		t.Errorf("an invalid plan = %d %q", code, stderr)
	}
	good := "summary: x\nworkstreams: [{name: e}]\ntasks: [{key: a, title: A, workstream: e}]\n"
	if code, stdout, _ := diatom(
		t,
		good,
		"task",
		"plan",
		"0002",
	); code != 0 ||
		!strings.Contains(stdout, "accepted") {
		t.Errorf("a valid plan = %d %q", code, stdout)
	}
	if code, _, _ := diatom(t, "", "task", "new-goal", "0002", "-title", "x"); code != 1 {
		t.Error("new-goal was accepted in a grilling session")
	}
}
