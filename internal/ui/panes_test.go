package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
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
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "ctrl+c":
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		case "shift+enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
		case "alt+a", "alt+c":
			msg = tea.KeyPressMsg{Code: rune(k[4]), Mod: tea.ModAlt}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "shift+space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModShift}
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

// openGoal selects a goal in the nav, the intake for queue.IntakeGoal, and
// opens its page.
func openGoal(t *testing.T, a *App, name string) {
	t.Helper()
	key(a, "esc", "esc", "esc", "esc")
	for i, e := range a.entries() {
		if e.row != nil && e.row.goal.Name == name {
			a.sel = i
			a.show()
			a.setFocus(partMain)
			return
		}
	}
	t.Fatalf("no %s in the nav", name)
}

func TestGoalPage(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	if title := a.View().WindowTitle; title != "diatom · vex · $0.00 today" {
		t.Errorf("titled %q", title)
	}
	openGoal(t, a, "set")
	out := plain(a.render())
	for _, want := range []string{"‹ set", "queued · set", "tasks 1 pending", "1 questions", "1 to review",
		"› a  Answer its 1 question", "r  Review its 1 hunks",
		"p  Park it", "0001 [engine] Add ward"} {
		if !strings.Contains(out, want) {
			t.Errorf("the goal's page lacks %q:\n%s", want, out)
		}
	}
	key(a, "p")
	if g, _ := f.store.Goal("set"); g.State != queue.GoalParked ||
		!strings.Contains(plain(a.render()), "p  Resume it") {
		t.Errorf("p left the goal %s:\n%s", g.State, plain(a.render()))
	}
	key(a, "p")
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Errorf("a second p left the goal %s", g.State)
	}
	// Past the actions, enter opens a task, and esc backs out to the nav.
	a.status.detail.sel = len(a.status.detailActions())
	key(a, "enter")
	if a.status.detail.task == nil || a.status.detail.task.id != "0001" {
		t.Fatalf("enter past the actions didn't open the task:\n%s", plain(a.render()))
	}
	key(a, "esc", "esc")
	if a.focus != partNav || a.status.detail == nil || a.status.detail.task != nil {
		t.Errorf("esc twice: focus %d", a.focus)
	}
}

func TestNavScrolls(t *testing.T) {
	f := newFixture(t)
	for i := range 20 {
		if err := f.store.CreateGoal(&queue.Goal{Name: fmt.Sprintf("g%02d", i),
			State: queue.GoalActive, Created: time.Unix(int64(i+10), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := newApp(t, f)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	for range 18 {
		key(a, "j")
	}
	out := plain(a.render())
	if !strings.Contains(out, "g15") || strings.Contains(out, "⏩ Next") {
		t.Errorf("the nav after moving down 18, in 12 lines:\n%s", out)
	}
	lines := a.navLines().list
	a.Update(tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if a.sel != lines[a.navTop].entry {
		t.Errorf("clicking the top line selected %d", a.sel)
	}
}

func TestGoalPageShowsWaitingGoal(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "later", State: queue.GoalActive,
		Created: time.Unix(5, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetAfter("later", []string{"set"}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	if out := plain(a.render()); !strings.Contains(out, "🔗 later") ||
		!strings.Contains(out, "    blocked") {
		t.Errorf("the nav:\n%s", out)
	}
	openGoal(t, a, "later")
	if out := plain(a.render()); !strings.Contains(out, "blocked · later") ||
		!strings.Contains(out, "blocked: waits for set to finish") {
		t.Errorf("the goal opened:\n%s", out)
	}
}

func TestGoalPageShowsRunningSession(t *testing.T) {
	f := newFixture(t)
	task, _ := f.store.Task("set", "0001")
	if err := f.store.Move("set", task, queue.Active); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.store.SessionsDir("set"), "20260101T000000Z-engine", "events.jsonl"),
		`{"type":"text","text":"old"}`+"\n"+`{"type":"tool","text":"Bash go test ./..."}`+"\n")
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	if out := plain(a.render()); !strings.Contains(out, "▶ engine") ||
		!strings.Contains(
			out,
			"Bash go test ./...",
		) || !strings.Contains(plain(a.nextCounts()), "🤖 1") {
		t.Errorf("running session missing:\n%s", out)
	}
}

func TestIntakeBeingSorted(t *testing.T) {
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
	// A running triage session.
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
	a, _ := newApp(t, f)
	openGoal(t, a, queue.IntakeGoal)
	out := plain(a.render())
	if !strings.Contains(out, "‹ Intake") || !strings.Contains(out, "1 being sorted") ||
		!strings.Contains(out, "1 waiting on your answers") ||
		!strings.Contains(out, "› ▶ planning Two goals, then.") ||
		!regexp.MustCompile(`▶ 0001 Triage: notes *\n`).MatchString(out) ||
		!strings.Contains(out, "0002 Triage: web · waiting on your answer") {
		t.Fatalf("intake opened, the task without the session's step:\n%s", out)
	}
	key(a, "p")
	if g, _ := f.store.Goal(queue.IntakeGoal); g.State != queue.GoalActive {
		t.Error("p parked the intake")
	}
	// The running session opens straight to its steps, and backs out to the
	// page.
	key(a, "enter")
	if out := plain(a.render()); !strings.Contains(out, "“ Two goals, then.") {
		t.Fatalf("the running session didn't open:\n%s", out)
	}
	key(a, "esc")
	if d := a.status.detail; d == nil || d.task != nil {
		t.Fatalf("esc from the session went to %+v", d)
	}
	// A click on a task opens it, as enter does.
	for y, l := range strings.Split(plain(a.render()), "\n") {
		if strings.Contains(l, "0002 Triage: web") {
			at := tea.Mouse{X: a.mainLeft() + 4, Y: y, Button: tea.MouseLeft}
			a.Update(tea.MouseClickMsg(at))
			a.Update(tea.MouseReleaseMsg(at))
		}
	}
	if d := a.status.detail; d == nil || d.task == nil || d.task.id != "0002" {
		t.Fatalf("the click on task 0002 opened %+v", d)
	}
	key(a, "esc", "k")
	// The task lists its Claude sessions; space opens one like enter.
	key(a, "enter")
	out = plain(a.render())
	if !strings.Contains(out, "› ▶ Claude running") || !strings.Contains(out, "2 steps") {
		t.Fatalf("task opened:\n%s", out)
	}
	key(a, " ")
	out = plain(a.render())
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
	a.Update(tickMsg{})
	out = plain(a.render())
	if !regexp.MustCompile(`› \d\d:\d\d:\d\d ▶ Start the poison goal`).MatchString(out) {
		t.Errorf("the open session didn't follow it:\n%s", out)
	}
	// A step opens to its command and output.
	key(a, "k", "k", "enter")
	out = plain(a.render())
	if !strings.Contains(out, "cat docs/todo.md") || !strings.Contains(out, "output after 2s") ||
		!strings.Contains(out, "## Ward") {
		t.Errorf("step opened:\n%s", out)
	}
	key(a, "esc", "esc", "esc", "esc")
	if a.focus != partNav {
		t.Errorf("esc four times didn't reach the nav: focus %d", a.focus)
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
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	a.status.detail.sel = len(a.status.detailActions())
	key(a, "enter")
	out := plain(a.render())
	if !strings.Contains(out, "✗ Claude completed") ||
		!strings.Contains(out, "no gate is configured") {
		t.Errorf("task:\n%s", out)
	}
}

func TestIntakeBox(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	if a.intake.area.Focused() {
		t.Error("the intake box has the cursor without the keyboard")
	}
	key(a, "i")
	if !a.intake.area.Focused() {
		t.Error("the intake box has the keyboard without the cursor")
	}
	typeText(a, "new goal:")
	key(a, "shift+enter")
	typeText(a, "implement the next set")
	if pending, _ := intake.Pending(intake.Dir(f.repo)); len(pending) != 0 {
		t.Fatal("shift+enter sent the intake")
	}
	key(a, "enter")
	if !strings.Contains(plain(a.render()), "Sent for triage.") {
		t.Errorf("intake after sending:\n%s", plain(a.render()))
	}
	typeText(a, "playtest: ward felt too strong")
	key(a, "enter")
	pending, err := intake.Pending(intake.Dir(f.repo))
	if err != nil || len(pending) != 2 || pending[0].Text != "new goal:\nimplement the next set" ||
		pending[1].Text != "playtest: ward felt too strong" || pending[1].Goal != "set" ||
		pending[1].Source != "window" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	key(a, "esc")
	a.Update(tickMsg{})
	if a.intake.area.Focused() ||
		!strings.Contains(plain(a.render()), "2 waiting for triage") {
		t.Errorf("intake after sending twice:\n%s", plain(a.render()))
	}
}

func TestPasteAndCut(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "enter", "tab")
	a.Update(tea.PasteMsg{Content: "pasted from elsewhere"})
	if a.next.answer.Value() != "pasted from elsewhere" {
		t.Fatalf("answer after a paste = %q", a.next.answer.Value())
	}
	// Selecting all and cutting empties it; the clipboard write is the
	// command key throws away, so the test leaves the real clipboard alone.
	key(a, "alt+a", "ctrl+x")
	if a.next.answer.Value() != "" {
		t.Errorf("answer after select all and cut = %q", a.next.answer.Value())
	}
	key(a, "tab")
	a.Update(tea.PasteMsg{Content: "a pasted intake"})
	key(a, "alt+a", "ctrl+x")
	if a.intake.area.Value() != "" {
		t.Errorf("intake after select all and cut = %q", a.intake.area.Value())
	}
}

func TestGoalPageApprovesAPlan(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := plan.NewGoal(ctx, f.store, "grim", "Grim Reminders", "", "", queue.Origin{},
		f.env.Now())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	openGoal(t, a, g.Name)
	if out := plain(a.render()); !strings.Contains(out, "grilling: the next round is queued") ||
		strings.Contains(out, "Review the plan") {
		t.Errorf("planning goal without a plan:\n%s", out)
	}
	p, _ := plan.Parse([]byte("summary: Do it.\nworkstreams: [{name: engine}]\n" +
		"tasks: [{key: a, title: Add ward, workstream: engine}]\n"))
	if err := plan.Save(f.store.GoalDir(g.Name), p); err != nil {
		t.Fatal(err)
	}
	a.Update(tickMsg{})
	out := plain(a.render())
	if !strings.Contains(out, "plan ready to approve: 1 workstreams, 1 tasks") ||
		!strings.Contains(out, "› a  Review the plan: 1 workstreams, 1 tasks") ||
		!strings.Contains(out, "[engine] Add ward") {
		t.Errorf("plan not shown:\n%s", out)
	}
	key(a, "a")
	if it := a.next.shown(); a.asking != g.Name || it == nil || it.kind != itemPlan ||
		a.next.area != areaBody {
		t.Fatalf("a opened %+v, asking %q, area %d", it, a.asking, a.next.area)
	}
	key(a, "a")
	if got, _ := f.store.Goal(g.Name); got.State != queue.GoalPlanning {
		t.Fatal("one a approved the plan")
	}
	key(a, "a")
	if got, _ := f.store.Goal(g.Name); got.State != queue.GoalActive {
		t.Errorf("after two a, goal is %s: %v", got.State, a.next.err)
	}
	if a.asking != "" || a.status.detail == nil || a.status.detail.goal != g.Name {
		t.Errorf("after approving, asking %q, detail %+v", a.asking, a.status.detail)
	}
}

// newPlan starts a goal in planning with a plan handed in.
func newPlan(t *testing.T, f *fixture, name string) {
	t.Helper()
	if _, err := plan.NewGoal(context.Background(), f.store, name, "Title of "+name, "", "",
		queue.Origin{}, f.env.Now()); err != nil {
		t.Fatal(err)
	}
	p, _ := plan.Parse([]byte("summary: Do " + name + ".\nworkstreams: [{name: engine}]\n" +
		"tasks: [{key: a, title: Add ward, workstream: engine}]\n"))
	if err := plan.Save(f.store.GoalDir(name), p); err != nil {
		t.Fatal(err)
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
	cmds := []tea.Cmd{cmd}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		cmds = batch
	} else if job, ok := msg.(jobMsg); ok {
		m.Update(job)
		return
	}
	// A job runs beside the tick that draws its log.
	msgs := make(chan tea.Msg, len(cmds))
	for _, c := range cmds {
		go func() { msgs <- c() }()
	}
	for range cmds {
		if job, ok := (<-msgs).(jobMsg); ok {
			m.Update(job)
			return
		}
	}
	t.Fatalf("%s started no job", k)
}

// makeReady finishes the fixture goal's task and approves its hunk, so it can
// land.
func makeReady(t *testing.T, f *fixture) {
	t.Helper()
	task, _ := f.store.Task("set", "0001")
	if err := f.store.Move("set", task, queue.Done); err != nil {
		t.Fatal(err)
	}
	items, err := review.Load(context.Background(), f.store, "set")
	if err != nil {
		t.Fatal(err)
	}
	rev := review.Store{Dir: f.store.GoalDir("set")}
	for _, it := range items {
		if _, err := rev.Decide(it.Hunk, review.Approve, nil, time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGoalPageEndsAndLandsAGoal(t *testing.T) {
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
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	s := a.status
	// Landing marks the goal done first, which refuses with a hunk unreviewed.
	press(t, a, "F")
	press(t, a, "F")
	if s.err == nil || !strings.Contains(s.err.Error(), "1 unreviewed") {
		t.Fatalf("F with a hunk unreviewed = %v", s.err)
	}
	press(t, a, "d")
	press(t, a, "d")
	if s.err == nil || !strings.Contains(s.err.Error(), "1 unreviewed") {
		t.Fatalf("d with a hunk unreviewed = %v", s.err)
	}
	makeReady(t, f)
	a.Update(tickMsg{})
	press(t, a, "d")
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Fatal("one d marked the goal done")
	}
	press(t, a, "d")
	out := plain(a.render())
	if g, _ := f.store.Goal("set"); g.State != queue.GoalDone ||
		!strings.Contains(
			out,
			"ready to land as 1 pull request · P merge into main · F open PRs",
		) {
		t.Fatalf("after two d, goal is %s: %v\n%s", g.State, s.err, out)
	}
	press(t, a, "P")
	press(t, a, "P")
	if tip, err := bare.RevParse(ctx, "main"); err != nil || s.err != nil ||
		!strings.Contains(plain(a.render()), "pushed, waiting to show up on origin/main") {
		t.Errorf(
			"after two P: main upstream %s, %v, %v\n%s",
			tip,
			err,
			s.err,
			plain(a.render()),
		)
	}
}

func TestFooterSaysWhenNothingCanStart(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	if foot := plain(a.footer()); !strings.Contains(foot, "no scheduler: reopen diatom") {
		t.Errorf("no scheduler: %q", foot)
	}
	unlock, err := f.store.LockScheduler()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	a.Update(tickMsg{})
	if foot := plain(a.footer()); strings.Contains(foot, "⚠") {
		t.Errorf("a healthy scheduler was reported: %q", foot)
	}
	if err := f.store.SetStuck("config: unknown setting autoApprove", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	a.Update(tickMsg{})
	if foot := plain(a.footer()); !strings.Contains(foot, "stuck since") ||
		!strings.Contains(foot, "unknown setting autoApprove") {
		t.Errorf("a stuck scheduler: %q", foot)
	}
}

func TestGoalStates(t *testing.T) {
	f := newFixture(t)
	s := NewStatus(context.Background(), f.env)
	state := func() string {
		s.reload()
		for _, r := range s.rows {
			if r.goal.Name == "set" {
				name, _ := goalStatus(r)
				return name
			}
		}
		return ""
	}
	// A pending task with no session running is queued.
	if got := state(); got != "queued" {
		t.Errorf("with a task ready: %q", got)
	}
	task, _ := f.store.Task("set", "0001")
	if err := f.store.Move("set", task, queue.Active); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "active" {
		t.Errorf("with a task running: %q", got)
	}
	if err := f.store.Move("set", task, queue.Done); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "reviewing" {
		t.Errorf("with every task done and a hunk to review: %q", got)
	}
	items, err := review.Load(context.Background(), f.store, "set")
	if err != nil {
		t.Fatal(err)
	}
	store := review.Store{Dir: f.store.GoalDir("set")}
	if _, err := store.Decide(items[0].Hunk, review.Approve, nil, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "ready to finish" {
		t.Errorf("with everything reviewed: %q", got)
	}
}

func TestShowsCosts(t *testing.T) {
	f := newFixture(t)
	// What was spent counts up to now.
	f.env.Now = time.Now
	for i, c := range []struct {
		tasks []string
		cost  float64
	}{{[]string{"0001"}, 1.5}, {[]string{"0001", "0002"}, 3}, {[]string{"0001"}, 0}} {
		id := fmt.Sprintf("2026010%dT000000Z-engine", i+1)
		dir := filepath.Join(f.store.SessionsDir("set"), id)
		if err := session.Create(dir, session.Spec{ID: id, Tasks: c.tasks}); err != nil {
			t.Fatal(err)
		}
		if c.cost == 0 {
			continue // still running: no cost yet
		}
		if err := session.WriteResult(
			dir,
			map[string]any{"usage": map[string]any{"costUSD": c.cost}},
		); err != nil {
			t.Fatal(err)
		}
		// Writing the commit message costs a little on top.
		if err := session.UpdateState(
			dir,
			func(st *session.State) { st.CommitCostUSD = 0.25 },
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.AddTask("set", &queue.Task{Title: "Add cards", Kind: queue.Planned,
		Workstream: "cards"}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	if title := a.View().WindowTitle; title != "diatom · vex · $5.00 today" ||
		!strings.Contains(plain(a.footer()), "D$5.00 · W$5.00 · M$5.00") {
		t.Errorf("titled %q, footer %q", title, a.footer())
	}
	openGoal(t, a, "set")
	out := plain(a.render())
	if !strings.Contains(out, "1 to review · $5.00") ||
		!strings.Contains(out, "Add ward · $3.38") ||
		!strings.Contains(out, "Add cards · $1.62") {
		t.Errorf("goal opened:\n%s", out)
	}
}

// TestClickAGoalsAction pins that a click on one of a goal's actions does
// it, as enter does, and that dragging over it only selects its text.
func TestClickAGoalsAction(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	park := func() tea.Mouse {
		for y, l := range strings.Split(plain(a.render()), "\n") {
			if strings.Contains(l, "Park it") {
				return tea.Mouse{X: a.mainLeft() + 4, Y: y, Button: tea.MouseLeft}
			}
		}
		t.Fatalf("no park action:\n%s", plain(a.render()))
		return tea.Mouse{}
	}
	at := park()
	a.Update(tea.MouseClickMsg(at))
	to := at
	to.X += 6
	a.Update(tea.MouseMotionMsg(to))
	a.Update(tea.MouseReleaseMsg(to))
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Fatal("a drag over the park action parked the goal")
	}
	at = park()
	a.Update(tea.MouseClickMsg(at))
	a.Update(tea.MouseReleaseMsg(at))
	if g, _ := f.store.Goal("set"); g.State != queue.GoalParked {
		t.Errorf("after a click on park, set is %s", g.State)
	}
}

func TestSessionShowsDiatomWrappingUp(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.store.SessionsDir("set"), "20260101T000000Z-engine")
	if err := session.Create(dir, session.Spec{ID: "20260101T000000Z-engine",
		Profile: "implementation", Model: "opus", Level: "high",
		Tasks: []string{"0001"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteResult(dir, map[string]any{"outcome": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := session.AppendEvent(
		dir,
		session.Settling(time.Unix(1, 0), "Running the gate `check`"),
	); err != nil {
		t.Fatal(err)
	}
	sv, err := loadSession(dir, "20260101T000000Z-engine")
	if err != nil {
		t.Fatal(err)
	}
	line := plain(sv.line(200))
	steps := plain(sv.render(200, 20))
	if !strings.Contains(line, "▶ diatom wrapping up") ||
		!strings.Contains(line, "Running the gate") ||
		!strings.Contains(steps, "▶ diatom: Running the gate `check`") ||
		!strings.Contains(steps, "opus · high · implementation profile · session") {
		t.Errorf("line %q, steps:\n%s", line, steps)
	}
}

func TestTaskListsItsDependencies(t *testing.T) {
	f := newFixture(t)
	if err := f.store.AddTask("set", &queue.Task{Title: "Card uses ward", Kind: queue.Planned,
		Workstream: "cards", DependsOn: []string{"0001", "0001"}}); err != nil {
		t.Fatal(err)
	}
	tv, err := loadTask(f.store, "set", "0002")
	if err != nil {
		t.Fatal(err)
	}
	s := NewStatus(context.Background(), f.env)
	out := plain(tv.render(s, 30))
	if !strings.Contains(out, "depends on:\n  ○ 0001 [engine] Add ward pending") ||
		strings.Count(out, "Add ward") != 1 {
		t.Errorf("task:\n%s", out)
	}
}

func TestNoticesStayUntilTheHumanActs(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	a.Update(jobMsg{err: errors.New("main moved on")})
	for range 3 {
		a.Update(tickMsg{})
	}
	if !strings.Contains(plain(a.render()), "main moved on") {
		t.Fatalf("the notice went with a reload:\n%s", plain(a.render()))
	}
	key(a, "j")
	if strings.Contains(plain(a.render()), "main moved on") {
		t.Error("the notice stayed after a key")
	}
}

func TestLandingOntoAConflictingMainGoesToAnAgent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := git.Repo{Dir: f.repo}
	if _, err := r.Run(ctx, "branch", "diatom/set/integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitFiles(ctx, "diatom/set/integration",
		map[string][]byte{"poison.go": []byte("package poison\n")}, "feat: poison"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitFiles(ctx, "main",
		map[string][]byte{"poison.go": []byte("package venom\n")}, "feat: venom"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveGoal(&queue.Goal{Name: "set", State: queue.GoalActive, Base: "main",
		Workstreams: []queue.Workstream{{Name: "engine"}}}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	press(t, a, "P")
	press(t, a, "P")
	if !strings.Contains(
		a.status.flash,
		"main has moved on and conflicts with set: an agent is merging it in",
	) {
		t.Errorf("flash = %q, err = %v", a.status.flash, a.status.err)
	}
	tasks, _ := f.store.Tasks("set")
	var merge *queue.Task
	for _, task := range tasks {
		if task.Merge != "" {
			merge = task
		}
	}
	if merge == nil || merge.Kind != queue.Conflict || merge.Workstream != "engine" {
		t.Errorf("tasks = %+v", tasks)
	}
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Errorf("the goal is %s", g.State)
	}
}

func TestAFailedLandingLeavesTheGoalActive(t *testing.T) {
	f := newFixture(t)
	// An integration branch with nothing main lacks has nothing to land.
	if _, err := (git.Repo{Dir: f.repo}).Run(context.Background(), "branch",
		"diatom/set/integration"); err != nil {
		t.Fatal(err)
	}
	makeReady(t, f)
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	press(t, a, "P")
	press(t, a, "P")
	if g, _ := f.store.Goal("set"); a.status.err == nil ||
		!strings.Contains(a.status.err.Error(), "no commits") || g.State != queue.GoalActive {
		t.Errorf("after a failed landing the goal is %s: %v", g.State, a.status.err)
	}
}

func TestLandingOnAFailingGateGoesToAnAgent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := git.Repo{Dir: f.repo}
	if _, err := r.Run(ctx, "branch", "diatom/set/integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitFiles(ctx, "diatom/set/integration",
		map[string][]byte{"poison.go": []byte("package poison\n")}, "feat: poison"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.repo, ".diatom", "config.toml"),
		"gate = \"echo 'lint: poison is unused' >&2; exit 1\"\n")
	makeReady(t, f)
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	press(t, a, "P")
	press(t, a, "P")
	out := plain(a.render())
	if !strings.Contains(a.status.flash, "fails the gate on main's tip, so it didn't land") ||
		!strings.Contains(out, "lint: poison is unused") || strings.Contains(out, "force") {
		t.Errorf("flash %q, err %v:\n%s", a.status.flash, a.status.err, out)
	}
	if g, _ := f.store.Goal("set"); g.State != queue.GoalActive {
		t.Errorf("the goal is %s", g.State)
	}
	tasks, _ := f.store.Tasks("set")
	repairs := 0
	for _, task := range tasks {
		if task.Kind == queue.GateRepair {
			repairs++
		}
	}
	if repairs != 1 {
		t.Errorf("%d gate repairs queued", repairs)
	}
}

func TestALandingInProgress(t *testing.T) {
	f := newFixture(t)
	makeReady(t, f)
	a, _ := newApp(t, f)
	// A goal ready to finish comes first.
	if it := a.next.shown(); it == nil || it.kind != itemFinish {
		t.Fatalf("Next shows %+v", it)
	}
	// As if P had started: the goal is being merged, and its log grows.
	a.status.busy, a.status.busyGoal, a.status.logGoal = "merging it into main", "set", "set"
	a.status.log = &jobLog{}
	_, _ = fmt.Fprintln(a.status.log, "Running the gate on diatom/set/final")
	_, _ = fmt.Fprintln(a.status.log, "ok  vex/engine  0.4s")
	a.next.reload()
	for _, it := range a.next.items {
		if it.kind == itemFinish {
			t.Error("Next still offers to finish the goal being landed")
		}
	}
	openGoal(t, a, "set")
	out := plain(a.render())
	for _, want := range []string{"▶ merging it into main…", "ok  vex/engine  0.4s"} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Merge it into main") || len(a.status.detailActions()) != 0 {
		t.Errorf("the page still offers actions:\n%s", out)
	}
	// Once it ends, the log stays until the human moves on.
	a.Update(jobMsg{flash: "merged set into main on origin"})
	if out := plain(a.render()); !strings.Contains(out, "ok  vex/engine  0.4s") ||
		!strings.Contains(out, "merged set into main") {
		t.Errorf("after the job:\n%s", out)
	}
	key(a, "j")
	if strings.Contains(plain(a.render()), "ok  vex/engine") {
		t.Error("the log stayed after a key")
	}
}

func TestParagraphsHang(t *testing.T) {
	lines := hang(strings.Repeat("word ", 20), 30)
	if len(lines) < 3 || strings.HasPrefix(lines[0], " ") ||
		!strings.HasPrefix(lines[1], "  word") {
		t.Errorf("hang = %q", lines)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 30 {
			t.Errorf("%q is wider than 30", l)
		}
	}
}

func TestGlyphsForAgentsAndGitWork(t *testing.T) {
	f := newFixture(t)
	task, _ := f.store.Task("set", "0001")
	if err := f.store.Move("set", task, queue.Active); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.store.SessionsDir("set"), "20260101T000000Z-engine")
	write(t, filepath.Join(dir, "events.jsonl"), `{"type":"tool","text":"Bash go test ./..."}`+"\n")
	a, _ := newApp(t, f)
	r := a.status.rows[0]
	r.questions, r.toReview = 0, 0
	if g := navGlyph(r, false); g != emoji("🤖") || plain(relevant(r)) != "running engine" {
		t.Errorf("an agent running = %s %q", g, plain(relevant(r)))
	}
	if g := navGlyph(r, true); g != emoji("🔀") {
		t.Errorf("a goal landing = %s", g)
	}
	// Its agent has ended, and diatom commits its work.
	write(t, filepath.Join(dir, "result.json"), `{"usage":{"costUSD":1}}`)
	a.reload()
	r = a.status.rows[0]
	r.questions, r.toReview = 0, 0
	if g := navGlyph(r, false); g != emoji("🔀") || plain(relevant(r)) != "committing engine" {
		t.Errorf("committing = %s %q", g, plain(relevant(r)))
	}
	if !strings.Contains(plain(a.nextCounts()), "🔀 1") || strings.Contains(a.nextCounts(), "🤖") {
		t.Errorf("next counts = %q", a.nextCounts())
	}
	r.activeWork, r.settling = []string{"cards", "engine"}, []string{"engine"}
	if got := plain(relevant(r)); got != "running cards; committing engine" {
		t.Errorf("both = %q", got)
	}
	if settling("") {
		t.Error("no session is settling")
	}
}
