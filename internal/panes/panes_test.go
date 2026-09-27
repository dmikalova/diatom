package panes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "shift+enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
		case "alt+a", "alt+c":
			msg = tea.KeyPressMsg{Code: rune(k[4]), Mod: tea.ModAlt}
		case "ctrl+x":
			msg = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
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
	for _, want := range []string{"set", "active", "1 pending", "1 questions", "1 to review"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if title := s.View().WindowTitle; title != "status · vex · focus the repo" {
		t.Errorf("titled %q", title)
	}

	// A goal opens to what can be done with it, then its tasks.
	key(s, "enter")
	fc, _ := f.env.Focus.Read()
	out = ansi.Strip(s.render())
	if fc.Goal != "set" || !strings.Contains(out, "0001 [engine] Add ward") ||
		!strings.Contains(
			out,
			"› D  Mark it done with work left: 1 tasks not done, 1 hunks to review",
		) ||
		!strings.Contains(out, "p  Park it") || !strings.Contains(out, "1 to review") {
		t.Errorf("focus = %+v, and the goal opened:\n%s", fc, out)
	}
	key(s, "j", "enter")
	if g, _ := f.store.Goal("set"); g.State != queue.GoalParked ||
		!strings.Contains(ansi.Strip(s.render()), "p  Resume it") {
		t.Errorf("enter on park left the goal %s:\n%s", g.State, s.render())
	}
	key(s, "j", "j", "enter")
	if s.detail.task == nil || s.detail.task.id != "0001" {
		t.Errorf("enter past the actions didn't open the task:\n%s", s.render())
	}
	key(s, "esc", "esc")
	if s.detail != nil || s.View().WindowTitle != "status · vex · focus set" {
		t.Errorf("esc didn't back out to the list, titled %q", s.View().WindowTitle)
	}
	key(s, "esc")
	if fc, _ := f.env.Focus.Read(); fc.Goal != "" {
		t.Errorf("after esc at the list, focus = %+v, want the repo", fc)
	}

	key(s, "p", "P")
	g, _ := f.store.Goal("set")
	if g.State != queue.GoalActive || !g.Pinned {
		t.Errorf("after p and P, goal = %+v", g)
	}
	s.Update(tickMsg{})
}

func TestStatusScrolls(t *testing.T) {
	f := newFixture(t)
	for i := range 10 {
		if err := f.store.CreateGoal(&queue.Goal{Name: fmt.Sprintf("g%02d", i),
			State: queue.GoalActive}); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStatus(context.Background(), f.env)
	s.Update(tea.WindowSizeMsg{Width: 60, Height: 6})
	for range 8 {
		key(s, "j")
	}
	out := ansi.Strip(s.render())
	if !strings.Contains(out, "› ") || strings.Count(out, "\n") > 5 ||
		strings.Contains(out, "g00") {
		t.Errorf("list after moving down 8, in 6 lines:\n%s", out)
	}
}

func TestStatusShowsWaitingGoal(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "later", State: queue.GoalActive}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetAfter("later", []string{"set"}); err != nil {
		t.Fatal(err)
	}
	if out := ansi.Strip(NewStatus(context.Background(), f.env).render()); !strings.Contains(out,
		"waiting for set to finish") {
		t.Errorf("status:\n%s", out)
	}
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
		`{"time":"2026-01-01T00:00:01Z","type":"tool","text":"Bash cat docs/todo.md","id":"t1",`+
			`"summary":"Read the todo list","detail":"cat docs/todo.md"}`+"\n"+
			`{"time":"2026-01-01T00:00:03Z","type":"result","id":"t1","detail":"## Poison\n## Ward"}`+"\n"+
			`{"time":"2026-01-01T00:00:04Z","type":"text","text":"Two goals, then."}`+"\n")
	s.reload()
	key(s, "enter")
	out = ansi.Strip(s.render())
	if !strings.Contains(out, "‹ Intake") ||
		!strings.Contains(out, "0001 Triage: notes · Two goals, then.") ||
		!strings.Contains(out, "0002 Triage: web · waiting on your answer") {
		t.Fatalf("intake opened:\n%s", out)
	}
	// The task lists its Claude sessions; space opens one like enter.
	key(s, "enter")
	out = ansi.Strip(s.render())
	if !strings.Contains(out, "› ▶ Claude running") || !strings.Contains(out, "2 steps") {
		t.Fatalf("task opened:\n%s", out)
	}
	key(s, " ")
	out = ansi.Strip(s.render())
	if !strings.Contains(out, "✓ Read the todo list · 2s") || !strings.Contains(out, "› ") ||
		!strings.Contains(out, "“ Two goals, then.") || strings.Contains(out, "cat docs/todo.md") {
		t.Fatalf("session opened:\n%s", out)
	}
	// It follows the session live, keeping to the latest step.
	if err := session.AppendEvent(dir, session.Event{
		Time:    time.Unix(1767225605, 0),
		Type:    "tool",
		ID:      "t2",
		Summary: "Start the poison goal",
		Detail:  "diatom task new-goal 0001",
	}); err != nil {
		t.Fatal(err)
	}
	s.Update(tickMsg{})
	out = ansi.Strip(s.render())
	if !regexp.MustCompile(`› \d\d:\d\d:\d\d ▶ Start the poison goal`).MatchString(out) {
		t.Errorf("the open session didn't follow it:\n%s", out)
	}
	// A step opens to its command and output.
	key(s, "k", "k", "enter")
	out = ansi.Strip(s.render())
	if !strings.Contains(out, "cat docs/todo.md") || !strings.Contains(out, "output after 2s") ||
		!strings.Contains(out, "## Ward") {
		t.Errorf("step opened:\n%s", out)
	}
	key(s, "esc", "esc", "esc", "esc")
	if s.detail != nil || !strings.Contains(ansi.Strip(s.render()), "intake 1 being sorted") {
		t.Errorf("esc four times didn't reach the list:\n%s", s.render())
	}
}

func TestSessionShowsWhyItWasntSettled(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.store.SessionsDir("set"), "20260101T000000Z-engine")
	if err := session.Create(dir, session.Spec{ID: "20260101T000000Z-engine",
		Tasks: []string{"0001"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteResult(
		dir,
		map[string]any{"outcome": "completed", "turns": 4},
	); err != nil {
		t.Fatal(err)
	}
	if err := session.UpdateState(dir, func(st *session.State) {
		st.Settled, st.Error = true, "no gate is configured"
	}); err != nil {
		t.Fatal(err)
	}
	s := NewStatus(context.Background(), f.env)
	key(s, "enter", "j", "j", "j", "enter")
	out := ansi.Strip(s.render())
	if !strings.Contains(out, "✗ Claude completed") ||
		!strings.Contains(out, "no gate is configured") {
		t.Errorf("task:\n%s", out)
	}
}

func TestQuestions(t *testing.T) {
	f := newFixture(t)
	q := NewQuestions(context.Background(), f.env)
	out := ansi.Strip(q.render())
	if q.View().WindowTitle != "questions · 1 open" ||
		!strings.Contains(out, "set\n› Does ward stack?") || strings.Contains(out, "questions") {
		t.Fatalf("questions, titled %q:\n%s", q.View().WindowTitle, out)
	}
	key(q, "enter")
	out = ansi.Strip(q.render())
	if !strings.Contains(out, "set › task 0001 Add ward") ||
		!strings.Contains(out, "It matters for poison.") ||
		q.View().WindowTitle != "questions · 1 open" {
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
	if q.View().WindowTitle != "questions · 0 open" || q.answering ||
		!strings.Contains(q.render(), "nothing else waiting") {
		t.Errorf("after the last answer, titled %q:\n%s", q.View().WindowTitle, q.render())
	}
}

func TestAnsweringMovesOn(t *testing.T) {
	f := newFixture(t)
	for _, text := range []string{"Second?", "Third?"} {
		if err := f.store.AddQuestion(
			"set",
			&queue.Question{Task: "0001", Text: text},
		); err != nil {
			t.Fatal(err)
		}
	}
	q := NewQuestions(context.Background(), f.env)
	key(q, "j", "enter") // the second
	typeText(q, "Two.")
	key(q, "enter")
	if out := ansi.Strip(q.render()); !q.answering || !strings.Contains(out, "Third?") ||
		!strings.Contains(out, "answered; task 0001") {
		t.Fatalf("after answering, not on the next question:\n%s", out)
	}
	typeText(q, "Three.")
	key(q, "enter") // the last: back round to the first still open
	if out := ansi.Strip(q.render()); !q.answering || !strings.Contains(out, "Does ward stack?") {
		t.Fatalf("after the last in the list, not on the first left:\n%s", out)
	}
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	answers := map[string]string{}
	for _, o := range open {
		answers[strings.TrimSpace(o.Text)] = o.Answer
	}
	if answers["Second?"] != "Two." || answers["Third?"] != "Three." ||
		answers["Does ward stack?\nIt matters for poison."] != "" {
		t.Errorf("answers = %v", answers)
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
	q := NewQuestions(context.Background(), f.env)
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
	if out := ansi.Strip(q.render()); !strings.Contains(out, "↓ more below") {
		t.Errorf("a long question didn't offer to scroll:\n%s", out)
	}
	key(q, "down", "down")
	if out := ansi.Strip(q.render()); !strings.Contains(out, "↑ more above") || q.scroll != 2 {
		t.Errorf("down didn't scroll the question (scroll %d):\n%s", q.scroll, out)
	}
	key(q, "up")
	if q.scroll != 1 {
		t.Errorf("up scrolled to %d", q.scroll)
	}
	// With a longer answer, the arrows are the answer's.
	typeText(q, "one")
	key(q, "shift+enter")
	typeText(q, "two")
	key(q, "up")
	if q.scroll != 1 {
		t.Errorf("up in a two-line answer scrolled the question to %d", q.scroll)
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
	if !strings.Contains(s.render(), "plan ready to sign off: 1 workstreams, 1 tasks") {
		t.Errorf("plan not listed:\n%s", s.render())
	}
	key(s, "enter")
	out := ansi.Strip(s.render())
	if !strings.Contains(out, "› s  Sign off the plan: 1 workstreams, 1 tasks") ||
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

// newPlan starts a goal in planning with a plan handed in.
func newPlan(t *testing.T, f *fixture, name string) {
	t.Helper()
	if _, err := plan.NewGoal(context.Background(), f.store, name, "Title of "+name, "",
		queue.Origin{}, f.env.Now()); err != nil {
		t.Fatal(err)
	}
	p, _ := plan.Parse([]byte("summary: Do " + name + ".\nworkstreams: [{name: engine}]\n" +
		"tasks: [{key: a, title: Add ward, workstream: engine}]\n"))
	if err := plan.Save(f.store.GoalDir(name), p); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionsSignOffPlans(t *testing.T) {
	f := newFixture(t)
	q := NewQuestions(context.Background(), f.env)
	newPlan(t, f, "grim")
	q.Update(tickMsg{})
	if title := q.View().WindowTitle; title != "questions · 1 open · 1 plan to sign off" ||
		!strings.Contains(
			ansi.Strip(q.render()),
			"grim\n  plan to sign off: 1 workstreams, 1 tasks · Title of grim",
		) {
		t.Fatalf("titled %q:\n%s", title, ansi.Strip(q.render()))
	}
	// Answering a question moves on to the plan waiting.
	key(q, "j", "enter")
	typeText(q, "Yes.")
	key(q, "enter")
	out := ansi.Strip(q.render())
	if !q.answering || !strings.Contains(out, "grim › plan to sign off Title of grim") ||
		!strings.Contains(out, "[engine] Add ward") {
		t.Fatalf("after answering, not on the plan:\n%s", out)
	}
	key(q, "enter")
	if g, _ := f.store.Goal("grim"); g.State != queue.GoalPlanning ||
		!strings.Contains(q.render(), "press enter again to sign off grim") {
		t.Fatal("one enter signed the plan off")
	}
	key(q, "enter")
	if g, _ := f.store.Goal("grim"); g.State != queue.GoalActive || q.err != nil ||
		!strings.Contains(q.render(), "grim is signed off and active; nothing else waiting") {
		t.Fatalf("after two enters, grim is %s, %v:\n%s", g.State, q.err, q.render())
	}

	// Typing sends the plan back with the changes.
	newPlan(t, f, "hex")
	q.reload()
	key(q, "enter")
	typeText(q, "Split the engine.")
	key(q, "enter")
	fb, err := intake.Pending(plan.FeedbackDir(f.store.GoalDir("hex")))
	if err != nil || len(fb) != 1 || fb[0].Text != "Split the engine." {
		t.Fatalf("feedback = %+v, %v", fb, err)
	}
	if g, _ := f.store.Goal("hex"); g.State != queue.GoalPlanning || q.answering ||
		!strings.Contains(
			q.render(),
			"sent back to hex's grilling with your changes; nothing else waiting",
		) {
		t.Errorf("hex is %s:\n%s", g.State, q.render())
	}
	if out := ansi.Strip(NewStatus(context.Background(), f.env).render()); !strings.Contains(out,
		"plan sent back with your changes") {
		t.Errorf("status of a plan sent back:\n%s", out)
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

func TestPasteAndCut(t *testing.T) {
	f := newFixture(t)
	q := NewQuestions(context.Background(), f.env)
	key(q, "enter")
	q.Update(tea.PasteMsg{Content: "pasted from elsewhere"})
	if q.area.Value() != "pasted from elsewhere" {
		t.Fatalf("answer after a paste = %q", q.area.Value())
	}
	// Selecting all and cutting empties it; the clipboard write is the
	// command key throws away, so the test leaves the real clipboard alone.
	key(q, "alt+a", "ctrl+x")
	if q.area.Value() != "" {
		t.Errorf("answer after select all and cut = %q", q.area.Value())
	}

	in := NewIntake(f.env)
	in.Update(tea.PasteMsg{Content: "a pasted intake"})
	key(in, "alt+a", "ctrl+x")
	if in.area.Value() != "" {
		t.Errorf("intake after select all and cut = %q", in.area.Value())
	}
}

func TestIntakeHidesItsCursorWhenNotFocused(t *testing.T) {
	f := newFixture(t)
	in := NewIntake(f.env)
	if !in.View().ReportFocus || !in.area.Focused() {
		t.Fatal("the intake doesn't track focus, or starts unfocused")
	}
	in.Update(tea.BlurMsg{})
	if in.area.Focused() {
		t.Error("the intake kept its cursor with another pane focused")
	}
	in.Update(tea.FocusMsg{})
	if !in.area.Focused() {
		t.Error("the intake didn't take its cursor back")
	}
}
