package queue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s := Open(t.TempDir())
	if err := s.CreateGoal(
		&Goal{
			Name:  "new-set",
			Title: "Implement the new set",
			State: GoalActive,
			Base:  "main",
			Workstreams: []Workstream{
				{Name: "engine"},
				{Name: "cards", DependsOn: []string{"engine"}},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGoalRoundTrip(t *testing.T) {
	s := newStore(t)
	g, err := s.Goal("new-set")
	if err != nil {
		t.Fatal(err)
	}
	if g.Title != "Implement the new set" || g.State != GoalActive || len(g.Workstreams) != 2 {
		t.Errorf("Goal = %+v", g)
	}
	if ws, ok := g.Workstream("cards"); !ok || ws.DependsOn[0] != "engine" {
		t.Errorf("Workstream(cards) = %+v, %v", ws, ok)
	}
	if _, ok := g.Workstream("web"); ok {
		t.Error("Workstream(web) found a workstream the goal lacks")
	}
	if g.IntegrationBranch() != "diatom/new-set/integration" ||
		g.WorkstreamBranch("cards") != "diatom/new-set/ws/cards" {
		t.Errorf("branches = %s, %s", g.IntegrationBranch(), g.WorkstreamBranch("cards"))
	}
	if err := s.CreateGoal(
		&Goal{Name: "new-set"},
	); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("CreateGoal of an existing goal: %v", err)
	}
	goals, err := s.Goals()
	if err != nil || len(goals) != 1 {
		t.Errorf("Goals = %v, %v", goals, err)
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"engine", "set-2", "a"} {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", "Engine", "-x", "a/b", "a b", ".."} {
		if err := ValidName(n); err == nil {
			t.Errorf("ValidName(%q) accepted it", n)
		}
	}
	s := Open(t.TempDir())
	if err := s.CreateGoal(
		&Goal{Name: "ok", Workstreams: []Workstream{{Name: "Bad"}}},
	); err == nil {
		t.Error("CreateGoal accepted an invalid workstream name")
	}
}

func TestTaskLifecycle(t *testing.T) {
	s := newStore(t)
	a := &Task{Title: "Add the ward keyword", Kind: Planned, Workstream: "engine", Body: "Details."}
	b := &Task{Title: "Fix merge", Kind: Conflict, Workstream: "engine"}
	for _, task := range []*Task{a, b} {
		if err := s.AddTask("new-set", task); err != nil {
			t.Fatal(err)
		}
	}
	if a.ID != "0001" || b.ID != "0002" {
		t.Errorf("ids = %s, %s", a.ID, b.ID)
	}
	if a.Profile != "implementation" || b.Profile != "mechanical" {
		t.Errorf("default profiles = %s, %s", a.Profile, b.Profile)
	}

	if err := s.Move("new-set", a, Active); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendNote(
		"new-set",
		a.ID,
		"Note",
		"Ward is in engine/keywords.go.\n",
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task("new-set", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Active ||
		got.Body != "Details.\n\n## Note\n\nWard is in engine/keywords.go.\n" {
		t.Errorf("task after note = %+v %q", got, got.Body)
	}

	got.Commits = []string{"abc123"}
	if err := s.Move("new-set", got, Done); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.taskPath("new-set", Active, a.ID)); !os.IsNotExist(err) {
		t.Errorf("task still in active after Move: %v", err)
	}

	// A new task's id counts the finished ones.
	c := &Task{Title: "Next", Kind: Planned}
	if err := s.AddTask("new-set", c); err != nil {
		t.Fatal(err)
	}
	if c.ID != "0003" {
		t.Errorf("third id = %s", c.ID)
	}

	all, err := s.Tasks("new-set")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].State != Done || all[0].Commits[0] != "abc123" ||
		all[1].State != Pending {
		t.Errorf("Tasks = %+v", all)
	}
	if _, err := s.Task("new-set", "0099"); err == nil {
		t.Error("Task of a missing id returned no error")
	}
}

func TestTempFilesAreSkipped(t *testing.T) {
	s := newStore(t)
	if err := s.AddTask("new-set", &Task{Title: "x", Kind: Planned}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.GoalDir("new-set"), "tasks", "pending")
	if err := os.WriteFile(
		filepath.Join(dir, ".0001.md.123"),
		[]byte("partial"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks("new-set")
	if err != nil || len(tasks) != 1 {
		t.Errorf("Tasks = %v, %v", tasks, err)
	}
}

func TestMalformedTask(t *testing.T) {
	s := newStore(t)
	dir := filepath.Join(s.GoalDir("new-set"), "tasks", "pending")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "0001.md"),
		[]byte("no frontmatter"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tasks("new-set"); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("Tasks error = %v", err)
	}
}

func TestQuestions(t *testing.T) {
	s := newStore(t)
	q := &Question{Task: "0001", Text: "Does ward stack?", Created: time.Unix(0, 0).UTC()}
	if err := s.AddQuestion("new-set", q); err != nil {
		t.Fatal(err)
	}
	if q.ID != "0001" {
		t.Errorf("question id = %s", q.ID)
	}
	if err := s.Answer("new-set", q.ID, " No, it does not. ", time.Unix(60, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	open, err := s.Questions("new-set", QuestionOpen)
	if err != nil || len(open) != 1 {
		t.Fatalf("open = %v, %v", open, err)
	}
	if open[0].Answer != "No, it does not." || open[0].Text != "Does ward stack?\n" ||
		open[0].Answered.IsZero() {
		t.Errorf("answered question = %+v", open[0])
	}
	if err := s.CloseQuestion("new-set", open[0]); err != nil {
		t.Fatal(err)
	}
	closed, _ := s.Questions("new-set", QuestionClosed)
	if len(closed) != 1 {
		t.Errorf("closed = %v", closed)
	}
	if err := s.Answer("new-set", q.ID, "again", time.Now()); err == nil {
		t.Error("Answer of a closed question returned no error")
	}
}

func TestKindDefaultProfile(t *testing.T) {
	want := map[Kind]string{
		GateRepair: "mechanical", Conflict: "mechanical", Revision: "implementation",
		Planned: "implementation", Triage: "planning", Grilling: "planning",
	}
	for k, p := range want {
		if got := k.DefaultProfile(); got != p {
			t.Errorf("%s.DefaultProfile() = %s, want %s", k, got, p)
		}
	}
}
