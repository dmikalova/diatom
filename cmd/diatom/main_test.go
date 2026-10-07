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
	// Never the real home config.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(dir)
	return dir
}

func diatom(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestStateMustBeIgnored(t *testing.T) {
	repo := inRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The developer's own global excludes would still ignore .diatom/.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	code, _, stderr := diatom(t, "", "task", "goals")
	if code != 1 || !strings.Contains(stderr, "not ignored") {
		t.Errorf("task goals = %d %q", code, stderr)
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

// TestTaskDoneReleasesTheRest pins that marking a task done in a session
// whose context has passed its budget hands its unstarted tasks on, and
// tells the agent to end the session; under the budget it does neither.
func TestTaskDoneReleasesTheRest(t *testing.T) {
	dir := t.TempDir()
	spec := session.Spec{ID: "s", Tasks: []string{"0001", "0002", "0003"}, ChainContext: 1000}
	if err := session.Create(dir, spec); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.EnvVar, dir)
	call := func(read int) {
		if err := session.AppendEvent(dir, session.Event{Type: session.EventCall, ID: "m",
			Call: &session.Call{Input: 10, CacheRead: read}}); err != nil {
			t.Fatal(err)
		}
	}
	call(500)
	if _, stdout, _ := diatom(
		t,
		"",
		"task",
		"done",
		"0001",
	); strings.Contains(
		stdout,
		"end the session",
	) {
		t.Errorf("under the budget: %q", stdout)
	}
	call(5000)
	if _, stdout, _ := diatom(t, "", "task", "ask", "0002", "Which?"); stdout == "" {
		t.Fatal("ask printed nothing")
	}
	_, stdout, _ := diatom(t, "", "task", "done", "0002")
	if !strings.Contains(stdout, "other tasks (0003) go to fresh sessions") {
		t.Errorf("over the budget: %q", stdout)
	}
	if r, _ := session.ReadReport(dir); !r.Released["0003"] || r.Released["0002"] {
		t.Errorf("released = %v", r.Released)
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
	if code, stdout, _ := diatom(t, "", "help"); code != 0 || !strings.Contains(stdout, "Usage:") {
		t.Error("help did not print usage")
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
		"-goal",
		"set",
		"-ws",
		"engine",
		"-title",
		"Weaken ward",
		"-after",
		"0003, 0004",
	); code != 0 {
		t.Fatalf("add-task: %s", stderr)
	}
	if code, _, stderr := diatom(
		t,
		"",
		"task",
		"new-goal",
		"0001",
		"-title",
		"Web UI",
	); code != 2 ||
		!strings.Contains(stderr, "-description") {
		t.Errorf("new-goal without a description = %d %q", code, stderr)
	}
	if code, _, stderr := diatom(
		t, "", "task", "new-goal", "0001", "-title", "Web UI", "-description", "A browser client.",
	); code != 0 {
		t.Fatalf("new-goal: %s", stderr)
	}
	// A repo with a tracker refuses a goal with no ticket, or a bad one.
	tracked := t.TempDir()
	if err := session.Create(tracked, session.Spec{ID: "s", Kind: queue.Triage,
		Tasks: []string{"0001"}, Tickets: "linear"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.EnvVar, tracked)
	if code, _, stderr := diatom(
		t, "", "task", "new-goal", "0001", "-title", "Web UI", "-description", "x",
	); code != 2 || !strings.Contains(stderr, "every goal needs -ticket") {
		t.Errorf("new-goal with no ticket = %d %q", code, stderr)
	}
	if code, _, stderr := diatom(
		t, "", "task", "new-goal", "0001", "-title", "Web UI", "-description", "x",
		"-ticket", "no ticket",
	); code != 2 || !strings.Contains(stderr, "such as DIP-4117") {
		t.Errorf("new-goal with a bad ticket = %d %q", code, stderr)
	}
	if code, _, stderr := diatom(
		t, "", "task", "new-goal", "0001", "-title", "Web UI", "-description", "x",
		"-ticket", "dip-4117",
	); code != 0 {
		t.Fatalf("new-goal with a ticket: %s", stderr)
	}
	if rep, err := session.ReadReport(tracked); err != nil || len(rep.Goals) != 1 ||
		rep.Goals[0].Ticket != "DIP-4117" {
		t.Errorf("the ticket was not kept: %+v, %v", rep.Goals, err)
	}
	t.Setenv(session.EnvVar, dir)
	planFile := filepath.Join(t.TempDir(), "plan.yaml")
	good := "summary: x\nworkstreams: [{name: e}]\ntasks: [{key: a, title: A, workstream: e}]\n"
	if err := os.WriteFile(planFile, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := diatom(
		t,
		"Decided.",
		"task",
		"new-goal",
		"0001",
		"-title",
		"ForgeKey",
		"-description",
		"x",
		"-plan",
		planFile,
	); code != 0 ||
		!strings.Contains(stdout, "for the human to sign off") {
		t.Fatalf("new-goal -plan = %d %q %q", code, stdout, stderr)
	}
	if err := os.WriteFile(planFile, []byte("summary: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := diatom(
		t,
		"",
		"task",
		"new-goal",
		"0001",
		"-title",
		"Bad",
		"-description",
		"x",
		"-plan",
		planFile,
	); code != 1 ||
		!strings.Contains(stderr, "not accepted") {
		t.Errorf("new-goal with an invalid plan = %d %q", code, stderr)
	}
	if code, _, stderr := diatom(
		t,
		"Split cards.",
		"task",
		"feedback",
		"0001",
		"-goal",
		"grim",
	); code != 0 {
		t.Fatalf("feedback: %s", stderr)
	}
	if code, _, _ := diatom(t, "", "task", "feedback", "0001", "-goal", "grim"); code != 2 {
		t.Error("feedback without text was accepted")
	}
	if code, _, _ := diatom(
		t,
		"",
		"task",
		"add-task",
		"0001",
		"-ws",
		"e",
		"-title",
		"x",
	); code != 2 {
		t.Error("add-task without -goal was accepted")
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
	if len(r.Adds) != 1 || r.Adds[0].Title != "Weaken ward" || r.Adds[0].Goal != "set" ||
		strings.Join(r.Adds[0].After, ",") != "0003,0004" ||
		r.Adds[0].Text != "Ward stops 1." ||
		len(r.Goals) != 2 || r.Goals[0].Description != "A browser client." ||
		r.Goals[1].Plan != good ||
		len(
			r.Feedback,
		) != 1 || r.Feedback[0].Goal != "grim" || r.Feedback[0].Text != "Split cards." {
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
	// A plan the human sent back is refused until it answers them.
	back := session.Spec{
		ID: "b", Kind: queue.Grilling, Tasks: []string{"0002"}, Feedback: true,
	}
	bdir := t.TempDir()
	if err := session.Create(bdir, back); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.EnvVar, bdir)
	if code, _, stderr := diatom(t, good, "task", "plan", "0002"); code != 1 ||
		!strings.Contains(stderr, "response:") {
		t.Errorf("a plan that ignores the feedback = %d %q", code, stderr)
	}
	answered := strings.Replace(good, "summary:", "response: I split the cards.\nsummary:", 1)
	if code, stdout, stderr := diatom(t, answered, "task", "plan", "0002"); code != 0 ||
		!strings.Contains(stdout, "accepted") {
		t.Errorf("an answered plan = %d %q %q", code, stdout, stderr)
	}
	t.Setenv(session.EnvVar, gdir)

	// A goal the human asked for can be started from any session; sorting
	// work into goals stays triage's.
	if code, stdout, stderr := diatom(
		t,
		"the brief",
		"task",
		"new-goal",
		"0002",
		"-title",
		"Split kinds",
		"-description",
		"Split the target kinds.",
	); code != 0 ||
		!strings.Contains(stdout, "Split kinds") {
		t.Errorf("new-goal in grilling = %d %q %q", code, stdout, stderr)
	}
	if code, _, stderr := diatom(t, "", "task", "add-task", "0002", "-goal", "set", "-ws", "e",
		"-title", "x"); code != 1 || !strings.Contains(stderr, "only for triage") {
		t.Errorf("add-task in grilling = %d %q", code, stderr)
	}
}

func TestOutsideARepo(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, cmd := range [][]string{{"open"}, {"task", "goals"}} {
		if code, _, stderr := diatom(
			t,
			"x",
			cmd...); code != 1 ||
			!strings.Contains(stderr, "not in a git repository") {
			t.Errorf("%s outside a repo = %d %q", cmd[0], code, stderr)
		}
	}
}

func TestTaskGoals(t *testing.T) {
	inRepo(t)
	s := queue.Open(".")
	if err := s.CreateGoal(&queue.Goal{Name: "set", Title: "Next set", State: queue.GoalActive,
		Workstreams: []queue.Workstream{{Name: "engine"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTask("set", &queue.Task{Title: "Add ward", Kind: queue.Planned,
		Workstream: "engine"}); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := diatom(t, "", "task", "goals")
	if code != 0 || stdout != "- `set`: Next set (active, 0 of 1 tasks done)\n" {
		t.Errorf("task goals = %d %q %q", code, stdout, stderr)
	}
	code, stdout, stderr = diatom(t, "", "task", "goals", "set")
	if code != 0 || !strings.Contains(stdout, "- 0001 pending [engine]: Add ward") {
		t.Errorf("task goals set = %d %q %q", code, stdout, stderr)
	}
	if code, _, stderr := diatom(t, "", "task", "goals", "web"); code != 1 ||
		!strings.Contains(stderr, "the goals are set") {
		t.Errorf("task goals web = %d %q", code, stderr)
	}
}
