package panes

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/registry"
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
	reg := registry.Registry{Path: filepath.Join(base, "state", "repos")}
	if err := reg.Add(repo); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: base, XDG: filepath.Join(base, "xdg")}
	write(t, filepath.Join(paths.XDG, "config.yaml"), "searchRoots: [~/code]\n")
	env := Env{
		Registry: reg,
		Focus:    focus.File{Path: filepath.Join(base, "state", "focus.yaml")},
		Paths:    paths,
		Now:      func() time.Time { return time.Unix(1000, 0).UTC() },
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
		case "ctrl+s":
			msg = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
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
	for _, want := range []string{"vex/set", "active", "1 pending", "1 questions", "1 to review", "focus nothing"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	key(s, "enter")
	fc, _ := f.env.Focus.Read()
	if fc.Repo != f.repo || fc.Goal != "set" || !strings.Contains(s.render(), "focus vex/set") {
		t.Errorf("focus = %+v", fc)
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

func TestStatusPicker(t *testing.T) {
	f := newFixture(t)
	s := NewStatus(context.Background(), f.env)
	key(s, "o")
	if !s.picking || len(s.repos) != 2 {
		t.Fatalf("picker repos = %v", s.repos)
	}
	typeText(s, "dot")
	if m := s.matches(); len(m) != 1 || !strings.HasSuffix(m[0], "dotfiles") {
		t.Fatalf("matches = %v", m)
	}
	key(s, "enter")
	fc, _ := f.env.Focus.Read()
	repos, _ := f.env.Registry.List()
	if s.picking || !strings.HasSuffix(fc.Repo, "dotfiles") || fc.Goal != "" || len(repos) != 2 {
		t.Errorf("after picking: focus %+v, repos %v", fc, repos)
	}
	key(s, "o", "esc")
	if s.picking {
		t.Error("esc did not close the picker")
	}
}

func TestQuestions(t *testing.T) {
	f := newFixture(t)
	q := NewQuestions(f.env)
	out := q.render()
	if !strings.Contains(out, "1 open") || !strings.Contains(out, "Does ward stack?") ||
		!strings.Contains(out, "Add ward") ||
		!strings.Contains(out, "It matters for poison.") {
		t.Fatalf("questions:\n%s", out)
	}
	key(q, "enter")
	typeText(q, "No, it never stacks.")
	key(q, "ctrl+s")
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if len(open) != 1 || open[0].Answer != "No, it never stacks." {
		t.Fatalf("answer = %+v", open)
	}
	if !strings.Contains(q.render(), "0 open") {
		t.Errorf("an answered question is still listed:\n%s", q.render())
	}
	key(q, "enter", "esc")
}

func TestIntake(t *testing.T) {
	f := newFixture(t)
	in := NewIntake(f.env)
	typeText(in, "playtest: ward felt too strong")
	key(in, "ctrl+s")
	if !strings.Contains(in.render(), "nothing is focused") {
		t.Errorf("intake without focus:\n%s", in.render())
	}

	if err := f.env.Focus.Write(focus.Focus{Repo: f.repo, Goal: "set"}); err != nil {
		t.Fatal(err)
	}
	key(in, "ctrl+s")
	pending, err := intake.Pending(intake.Dir(f.repo, "set"))
	if err != nil || len(pending) != 1 || pending[0].Text != "playtest: ward felt too strong" ||
		pending[0].Source != "pane" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if !strings.Contains(in.render(), "1 waiting for triage") {
		t.Errorf("intake after queueing:\n%s", in.render())
	}

	// With only a repo focused, a new goal goes to the repo's intake.
	if err := f.env.Focus.Write(focus.Focus{Repo: f.repo}); err != nil {
		t.Fatal(err)
	}
	typeText(in, "new goal: implement the next set")
	key(in, "ctrl+s")
	if repoLevel, _ := intake.Pending(intake.Dir(f.repo, "")); len(repoLevel) != 1 {
		t.Errorf("repo intake = %+v", repoLevel)
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
