package panes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

type fixture struct {
	t     *testing.T
	env   Env
	repo  string
	store *queue.Store
}

// newFixture makes one known repo with an active goal holding a task, a
// commit and an open question, under a code root the picker searches.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	code := filepath.Join(base, "code")
	repo := filepath.Join(code, "org", "vex")
	for _, dir := range []string{repo, filepath.Join(code, "org", "dotfiles", ".git"), filepath.Join(base, "xdg")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := git.Repo{Dir: repo}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(repo, ".git", "info", "exclude"), ".diatom/\n")
	write(t, filepath.Join(repo, "ward.go"), "package ward\n")
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "feat: ward")
	if err != nil {
		t.Fatal(err)
	}
	store := queue.Open(repo)
	if err := store.CreateGoal(
		&queue.Goal{Name: "set", State: queue.GoalActive, Base: "main"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTask(
		"set",
		&queue.Task{Title: "Add ward", Kind: queue.Planned, Workstream: "engine",
			Commits: []string{sha}},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AddQuestion(
		"set",
		&queue.Question{Task: "0001", Text: "Does ward stack?\nIt matters for poison."},
	); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: base, XDG: filepath.Join(base, "xdg")}
	env := Env{
		Store: store,
		Focus: focus.In(repo),
		Paths: paths,
		Now:   func() time.Time { return time.Unix(1000, 0).UTC() },
	}
	return &fixture{t: t, env: env, repo: repo, store: store}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func key(m tea.Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "shift+enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
		default:
			r, _ := utf8.DecodeRuneInString(k)
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		m.Update(msg)
	}
}

func typeText(m tea.Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	s := NewStatus(context.Background(), f.env)
	out := s.render()
	for _, want := range []string{"vex", "set", "active", "1 pending", "1 questions", "1 to review", "focus the repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	key(s, "enter")
	fc, _ := f.env.Focus.Read()
	if fc.Goal != "set" || !strings.Contains(ansi.Strip(s.render()), "0001 [engine] Add ward") {
		t.Errorf("focus = %+v, and the goal opened:\n%s", fc, s.render())
	}
	key(s, "esc")
	if !strings.Contains(s.render(), "focus set") {
		t.Errorf("esc didn't back out to the list:\n%s", s.render())
	}
	key(s, "esc")
	if fc, _ := f.env.Focus.Read(); fc.Goal != "" {
		t.Errorf("after esc at the list, focus = %+v, want the repo", fc)
	}

	key(s, "p")
	if g, _ := f.store.Goal("set"); g.State != queue.GoalParked {
		t.Errorf("after p, goal is %s", g.State)
	}
	key(s, "p", "P")
	g, _ := f.store.Goal("set")
	if g.State != queue.GoalActive || !g.Pinned {
		t.Errorf("after p and P, goal = %+v", g)
	}
	s.Update(tickMsg{})
}

func TestStatusShowsRunningSession(t *testing.T) {
	f := newFixture(t)
	task, _ := f.store.Task("set", "0001")
	if err := f.store.Move("set", task, queue.Active); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.store.SessionsDir("set"), "20260101T000000Z-engine", "events.jsonl"),
		`{"type":"text","text":"old"}`+"\n"+`{"type":"tool","text":"Bash go test ./..."}`+"\n")
	out := NewStatus(context.Background(), f.env).render()
	if !strings.Contains(out, "▶ engine") || !strings.Contains(out, "Bash go test ./...") {
		t.Errorf("running session missing:\n%s", out)
	}
}

func TestStatusShowsIntakeBeingSorted(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(
		&queue.Goal{Name: queue.IntakeGoal, State: queue.GoalActive},
	); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Triage: notes", "Triage: web"} {
		if err := f.store.AddTask(
			queue.IntakeGoal,
			&queue.Task{Title: title, Kind: queue.Triage},
		); err != nil {
			t.Fatal(err)
		}
	}
	task, _ := f.store.Task(queue.IntakeGoal, "0002")
	if err := f.store.Move(queue.IntakeGoal, task, queue.Blocked); err != nil {
		t.Fatal(err)
	}
	s := NewStatus(context.Background(), f.env)
	out := ansi.Strip(s.render())
	if !strings.Contains(out, "intake 1 being sorted") ||
		!strings.Contains(out, "1 waiting on your answers") ||
		strings.Contains(out, queue.IntakeGoal) {
		t.Errorf("status:\n%s", out)
	}
	key(s, "p")
	if !strings.Contains(s.render(), "intake isn't a goal") {
		t.Error("p parked the intake")
	}

	// A running triage session, opened from the intake row.
	busy, _ := f.store.Task(queue.IntakeGoal, "0001")
	if err := f.store.Move(queue.IntakeGoal, busy, queue.Active); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.store.SessionsDir(queue.IntakeGoal), "20260101T000000Z-planning")
	if err := session.Create(
		dir,
		session.Spec{ID: "20260101T000000Z-planning", Tasks: []string{"0001"}},
	); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "events.jsonl"),
		`{"time":"2026-01-01T00:00:01Z","type":"tool","text":"Bash cat docs/todo.md"}`+"\n"+
			`{"time":"2026-01-01T00:00:02Z","type":"text","text":"Two goals, then."}`+"\n")
	s.reload()
	key(s, "enter")
	out = ansi.Strip(s.render())
	if !strings.Contains(out, "‹ Intake") ||
		!strings.Contains(out, "0001 Triage: notes · Two goals, then.") ||
		!strings.Contains(out, "0002 Triage: web · waiting on your answer") {
		t.Fatalf("intake opened:\n%s", out)
	}
	key(s, "enter")
	out = ansi.Strip(s.render())
	if !strings.Contains(out, "▶ running") || !strings.Contains(out, "Bash cat docs/todo.md") ||
		!strings.Contains(out, "Two goals, then.") {
		t.Fatalf("task opened:\n%s", out)
	}
	// It follows the session live.
	write(
		t,
		filepath.Join(dir, "events.jsonl"),
		`{"time":"2026-01-01T00:00:03Z","type":"tool","text":"Bash diatom task new-goal 0001"}`+"\n",
	)
	s.Update(tickMsg{})
	if !strings.Contains(ansi.Strip(s.render()), "diatom task new-goal") {
		t.Error("the open task didn't follow the session")
	}
	key(s, "esc", "esc")
	if s.detail != nil || !strings.Contains(ansi.Strip(s.render()), "intake 1 being sorted") {
		t.Errorf("esc twice didn't reach the list:\n%s", s.render())
	}
}

func TestQuestions(t *testing.T) {
	f := newFixture(t)
	q := NewQuestions(f.env)
	out := ansi.Strip(q.render())
	if q.View().WindowTitle != "questions · 1 open · enter answers" ||
		!strings.Contains(out, "set\n› Does ward stack?") || strings.Contains(out, "questions") {
		t.Fatalf("questions, titled %q:\n%s", q.View().WindowTitle, out)
	}
	key(q, "enter")
	out = ansi.Strip(q.render())
	if !strings.Contains(out, "set › task 0001 Add ward") ||
		!strings.Contains(out, "It matters for poison.") ||
		!strings.Contains(q.View().WindowTitle, "esc back") {
		t.Fatalf("question opened:\n%s", out)
	}
	key(q, "enter") // nothing typed: nothing sent
	if !q.answering {
		t.Fatal("an empty answer closed the question")
	}
	typeText(q, "No, it never")
	key(q, "shift+enter")
	typeText(q, "stacks.")
	key(q, "enter")
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if len(open) != 1 || open[0].Answer != "No, it never\nstacks." {
		t.Fatalf("answer = %+v", open)
	}
	if q.View().WindowTitle != "questions · 0 open" {
		t.Errorf("an answered question is still counted: %q", q.View().WindowTitle)
	}
}

func TestQuestionsScroll(t *testing.T) {
	f := newFixture(t)
	for i := range 20 {
		if err := f.store.AddQuestion("set", &queue.Question{Task: "0001",
			Text: fmt.Sprintf("Question %02d?", i)}); err != nil {
			t.Fatal(err)
		}
	}
	q := NewQuestions(f.env)
	q.Update(tea.WindowSizeMsg{Width: 60, Height: 8})
	for range 15 {
		key(q, "j")
	}
	out := ansi.Strip(q.render())
	if !strings.Contains(out, "› Question 14?") || strings.Count(out, "\n") > 7 ||
		strings.Contains(out, "Does ward stack?") {
		t.Errorf("list after moving down 15, in 8 lines:\n%s", out)
	}
	for range 15 {
		key(q, "k")
	}
	if out := ansi.Strip(q.render()); !strings.HasPrefix(out, "set\n› Does ward stack?") {
		t.Errorf("list back at the top:\n%s", out)
	}
	// A long question scrolls above its answer.
	long := strings.Repeat("A long line of question text. ", 40)
	if err := f.store.AddQuestion("set", &queue.Question{Task: "0001", Text: long}); err != nil {
		t.Fatal(err)
	}
	q.reload()
	for range 21 {
		key(q, "j")
	}
	key(q, "enter")
	if out := ansi.Strip(q.render()); !strings.Contains(out, "pgdn for more") {
		t.Errorf("a long question didn't offer to scroll:\n%s", out)
	}
}

func TestIntake(t *testing.T) {
	f := newFixture(t)
	in := NewIntake(f.env)
	// With no goal focused, the intake goes to triage with no hint.
	typeText(in, "new goal:")
	key(in, "shift+enter")
	typeText(in, "implement the next set")
	if pending, _ := intake.Pending(intake.Dir(f.repo)); len(pending) != 0 {
		t.Fatal("shift+enter sent the intake")
	}
	key(in, "enter")
	if out := ansi.Strip(in.render()); !strings.Contains(out, "ent for triage.") ||
		!strings.Contains(out, "Looking at the repo") {
		t.Errorf("intake after queueing:\n%s", in.render())
	}

	// With a goal focused, it goes with the goal as a hint.
	if err := f.env.Focus.Write(focus.Focus{Goal: "set"}); err != nil {
		t.Fatal(err)
	}
	typeText(in, "playtest: ward felt too strong")
	key(in, "enter")
	pending, err := intake.Pending(intake.Dir(f.repo))
	if err != nil || len(pending) != 2 || pending[0].Goal != "" ||
		pending[0].Text != "new goal:\nimplement the next set" ||
		pending[1].Text != "playtest: ward felt too strong" || pending[1].Goal != "set" ||
		pending[1].Source != "pane" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if out := ansi.Strip(in.render()); !strings.Contains(out, "2 waiting for triage") ||
		!strings.Contains(out, "Looking at goal set") || strings.Contains(out, "intake") {
		t.Errorf("intake after queueing twice:\n%s", in.render())
	}
	in.Update(tickMsg{})
}

func TestStatusSignsOffAPlan(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := plan.NewGoal(ctx, f.store, "grim", "Grim Reminders", "", queue.Origin{}, f.env.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := NewStatus(ctx, f.env)
	s.sel = slices.IndexFunc(s.rows, func(r goalRow) bool { return r.goal.Name == g.Name })
	if !strings.Contains(s.render(), "grilling: the next round is queued") {
		t.Errorf("planning goal without a plan:\n%s", s.render())
	}
	key(s, "s")
	if !strings.Contains(s.render(), "no plan to sign off") {
		t.Error("s signed off a goal without a plan")
	}

	p, _ := plan.Parse(
		[]byte(
			"summary: Do it.\nworkstreams: [{name: engine}]\ntasks: [{key: a, title: Add ward, workstream: engine}]\n",
		),
	)
	if err := plan.Save(f.store.GoalDir(g.Name), p); err != nil {
		t.Fatal(err)
	}
	s.reload()
	key(s, "v")
	out := s.render()
	if !strings.Contains(out, "plan ready: 1 workstreams, 1 tasks") ||
		!strings.Contains(out, "[engine] Add ward") {
		t.Errorf("plan not shown:\n%s", out)
	}
	key(s, "s")
	if got, _ := f.store.Goal(
		g.Name,
	); got.State != queue.GoalPlanning ||
		!strings.Contains(s.render(), "press s again") {
		t.Fatal("one s signed the plan off")
	}
	key(s, "s")
	if got, _ := f.store.Goal(g.Name); got.State != queue.GoalActive {
		t.Errorf("after two s, goal is %s: %v", got.State, s.err)
	}
}

// press sends one key and runs the background job it starts, if any, to
// its end.
func press(t *testing.T, m tea.Model, k string) {
	t.Helper()
	r, _ := utf8.DecodeRuneInString(k)
	_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: k})
	if cmd == nil {
		return
	}
	msg := cmd()
	if _, ok := msg.(jobMsg); !ok {
		t.Fatalf("%s started %T, not a job", k, msg)
	}
	m.Update(msg)
}

func TestStatusEndsAndLandsAGoal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := git.Repo{Dir: f.repo}
	if _, err := r.Run(ctx, "branch", "diatom/set/integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitFiles(
		ctx,
		"diatom/set/integration",
		map[string][]byte{"poison.go": []byte("package poison\n")},
		"feat: poison",
	); err != nil {
		t.Fatal(err)
	}
	bare := git.Repo{Dir: t.TempDir()}
	if _, err := bare.Run(ctx, "init", "--bare", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "remote", "add", "origin", bare.Dir); err != nil {
		t.Fatal(err)
	}
	s := NewStatus(ctx, f.env)

	press(t, s, "F")
	if !strings.Contains(s.render(), "isn't done") {
		t.Error("F landed a goal that isn't done")
	}
	press(t, s, "d")
	press(t, s, "d")
	if s.err == nil || !strings.Contains(s.err.Error(), "1 unreviewed") {
		t.Fatalf("d with a hunk unreviewed = %v", s.err)
	}
	press(t, s, "D")
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Fatal("one D marked the goal done")
	}
	press(t, s, "D")
	out := s.render()
	if g, _ := f.store.Goal("set"); g.State != queue.GoalDone ||
		!strings.Contains(out, "laid out as 1 pull request, not landed yet · F open PRs · U push") {
		t.Fatalf("after two D, goal is %s: %v\n%s", g.State, s.err, out)
	}

	press(t, s, "U")
	press(t, s, "U")
	if tip, err := bare.RevParse(ctx, "main"); err != nil || s.err != nil ||
		!strings.Contains(s.render(), "pushed, waiting to show up on origin/main") {
		t.Errorf("after two U: main upstream %s, %v, %v\n%s", tip, err, s.err, s.render())
	}
}
