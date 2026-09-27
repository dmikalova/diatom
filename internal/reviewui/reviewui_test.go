package reviewui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
)

func TestWordDiff(t *testing.T) {
	oldCells, newCells := plain("\treturn foo(bar, 1)"), plain("\treturn foo(baz, 1)")
	wordDiff(oldCells, newCells)
	changed := func(cells []cell) string {
		var b strings.Builder
		for _, c := range cells {
			if c.changed {
				b.WriteRune(c.r)
			}
		}
		return b.String()
	}
	if got := changed(oldCells); got != "bar" {
		t.Errorf("old changed = %q, want bar", got)
	}
	if got := changed(newCells); got != "baz" {
		t.Errorf("new changed = %q, want baz", got)
	}

	long := plain(strings.Repeat("a ", 400))
	other := plain(strings.Repeat("b ", 400))
	wordDiff(long, other)
	if !long[0].changed || !other[len(other)-1].changed {
		t.Error("a line too long to diff word by word was not marked changed as a whole")
	}
}

func TestHighlight(t *testing.T) {
	got := highlight("x.go", []string{"func main() {", `	s := "multi`, `line"`, "}"})
	if len(got) != 4 {
		t.Fatalf("highlight returned %d lines", len(got))
	}
	if got[0][0].fg != red {
		t.Errorf("func colored %d, want the keyword color", got[0][0].fg)
	}
	if plain := highlight("README", []string{"hello"}); plain[0][0].fg != noColor {
		t.Error("a file with no known language was colored")
	}
}

func TestRenderCells(t *testing.T) {
	cells := plain("a\tb")
	cells[2].changed = true
	out := renderCells(cells, gitdiff.OpAdd, 10)
	if len(out) != 1 || !strings.Contains(out[0], sgr(shade.add.bg()...)+"a   ") ||
		!strings.Contains(out[0], sgr(shade.addWord.bg()...)+"b") ||
		ansi.StringWidth(out[0]) != 10 {
		t.Errorf("renderCells = %q", out)
	}
	// Syntax colors stay on a deleted line's tint, and a context line isn't
	// tinted or filled out.
	del := highlight("x.go", []string{"func x()"})[0]
	del[5].changed = true
	out = renderCells(del, gitdiff.OpDelete, 20)
	if !strings.Contains(out[0], sgr(append(shade.del.bg(), fgCode(red))...)+"func") ||
		!strings.Contains(out[0], sgr(append(shade.delWord.bg(), fgCode(green))...)+"x") {
		t.Errorf("a deleted line = %q", out)
	}
	if ctx := renderCells(plain("same"), gitdiff.OpContext, 20); ansi.StringWidth(ctx[0]) != 4 {
		t.Errorf("a context line = %q", ctx)
	}
	SetDark(true)
	if shade != darkShades {
		t.Error("a dark terminal keeps the light shades")
	}
	SetDark(false)
	// A long line wraps at a space where it can, and anywhere where it can't.
	got := renderCells(plain("out <- chooser.ChooseOption(src)"), gitdiff.OpContext, 16)
	var rows []string
	for _, r := range got {
		rows = append(rows, ansi.Strip(r))
	}
	if strings.Join(rows, "|") != "out <- |chooser.ChooseOp|tion(src)" {
		t.Errorf("wrapped = %q", rows)
	}
}

func TestLongLinesWrap(t *testing.T) {
	m := &Model{width: 40}
	l := line{op: gitdiff.OpAdd, newN: 7, cells: plain(strings.Repeat("word ", 12))}
	rows := m.row(l, false, 1, 40)
	if len(rows) != 2 {
		t.Fatalf("rows = %q", rows)
	}
	first, second := ansi.Strip(rows[0]), ansi.Strip(rows[1])
	if !strings.HasPrefix(first, "   7 + word") || !strings.HasPrefix(second, "     + word") ||
		len(first) > 40 {
		t.Errorf("rows:\n%s\n%s", first, second)
	}
}

type fixture struct {
	t     *testing.T
	repo  git.Repo
	store *queue.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	r := git.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, repo: r, store: queue.Open(r.Dir)}
	// diatom relies on .diatom/ being ignored, which a developer's global
	// excludes do but CI's do not.
	if err := os.MkdirAll(filepath.Join(r.Dir, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(".git/info/exclude", ".diatom/\n")
	f.write("ward.go", body("b", "u"))
	f.commit("chore: start")
	if err := f.store.CreateGoal(&queue.Goal{Name: "set", State: queue.GoalActive}); err != nil {
		t.Fatal(err)
	}
	return f
}

// body is twenty lines with two of them named, far enough apart to make two
// hunks when both change.
func body(first, second string) string {
	var b strings.Builder
	for i := range 20 {
		switch i {
		case 1:
			b.WriteString("var " + first + " = 1\n")
		case 18:
			b.WriteString("var " + second + " = 2\n")
		default:
			b.WriteString("// line\n")
		}
	}
	return b.String()
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo.Dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(msg string) string {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.repo.StageAll(ctx); err != nil {
		f.t.Fatal(err)
	}
	sha, err := f.repo.Commit(ctx, msg)
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) task(task *queue.Task) {
	f.t.Helper()
	if err := f.store.AddTask("set", task); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) model() *Model {
	f.t.Helper()
	m, err := New(context.Background(), f.store, "set")
	if err != nil {
		f.t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		default:
			r, _ := utf8.DecodeRuneInString(k)
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		m.Update(msg)
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestReviewFlow(t *testing.T) {
	f := newFixture(t)
	f.write("ward.go", body("ward", "poison"))
	sha := f.commit("feat: ward and poison")
	f.task(
		&queue.Task{
			Title:      "Add ward",
			Kind:       queue.Planned,
			Workstream: "engine",
			Commits:    []string{sha},
		},
	)

	m := f.model()
	out := m.render()
	for _, want := range []string{"2 to review", "feat: ward and poison", "task 0001: Add ward", "ward.go", "hunk 1 of 2",
		"unreviewed"} {
		if !strings.Contains(out, want) {
			t.Errorf("first screen lacks %q:\n%s", want, out)
		}
	}
	if l := m.lines[m.cursor]; l.op == gitdiff.OpContext {
		t.Error("the cursor did not start on the first change")
	}

	// Comment on the cursor line, then reject.
	press(m, "c")
	typeText(m, "call it ward2")
	press(m, "enter")
	if len(m.drafts) != 1 || !strings.Contains(m.render(), "💬 call it ward2") {
		t.Fatalf("drafts = %+v", m.drafts)
	}
	first := m.items[m.cur].Hunk
	press(m, "r")
	rec, _ := m.rev.Load(sha)
	if r := rec.Hunks[first.ID]; r == nil || r.Decision != review.Reject ||
		r.Comments[0].Text != "call it ward2" {
		t.Fatalf("record = %+v", rec.Hunks)
	}
	if m.cur < 0 || m.items[m.cur].ID == first.ID || !strings.Contains(m.render(), "1 to review") {
		t.Fatalf("after rejecting, on screen = %d", m.cur)
	}

	press(m, "a")
	if m.cur != -1 || !strings.Contains(m.render(), "Nothing left to review") {
		t.Fatalf("after approving the last hunk, on screen = %d:\n%s", m.cur, m.render())
	}

	// Step back to the approval and defer it instead.
	press(m, "u")
	if m.cur < 0 || m.items[m.cur].Record.Decision != review.Approve {
		t.Fatalf("step back = %d", m.cur)
	}
	press(m, "d")
	if m.cur < 0 || m.items[m.cur].Record.Decision != review.Defer ||
		!strings.Contains(m.render(), "(1 deferred)") {
		t.Errorf("a deferred hunk that is the only one left should stay on screen:\n%s", m.render())
	}
	press(m, "u", "u")
	if m.cur < 0 || m.items[m.cur].ID != first.ID || len(m.drafts) != 1 {
		t.Errorf(
			"stepping back twice should reach the rejected hunk with its comment, got %d",
			m.cur,
		)
	}

	press(m, "esc", "q")
}

func TestCommentEditing(t *testing.T) {
	f := newFixture(t)
	f.write("ward.go", body("ward", "u"))
	sha := f.commit("feat: ward")
	f.task(
		&queue.Task{
			Title:      "Add ward",
			Kind:       queue.Planned,
			Workstream: "engine",
			Commits:    []string{sha},
		},
	)
	m := f.model()

	press(m, "c")
	typeText(m, "never mind")
	press(m, "esc")
	if len(m.drafts) != 0 || m.editing {
		t.Error("esc kept the comment")
	}
	press(m, "c")
	typeText(m, "keep")
	press(m, "enter", "c")
	if m.input.Value() != "keep" {
		t.Errorf("editing a commented line starts from %q", m.input.Value())
	}
	press(m, "esc", "x")
	if len(m.drafts) != 0 {
		t.Error("x did not drop the comment")
	}
	press(m, "down", "n", "p")
	if m.cur < 0 {
		t.Error("skipping lost the only hunk")
	}
}

func TestFixupShowsCausesAndCombinedView(t *testing.T) {
	f := newFixture(t)
	f.write("ward.go", body("ward", "u"))
	original := f.commit("feat: ward")
	f.write("ward.go", body("warden", "u"))
	fixup := f.commit("fixup! feat: ward")
	f.task(
		&queue.Task{
			Title:      "Add ward",
			Kind:       queue.Planned,
			Workstream: "engine",
			Commits:    []string{original},
		},
	)
	f.task(
		&queue.Task{Title: "Revise", Kind: queue.Revision, Workstream: "engine", Revises: original,
			Commits: []string{fixup}, Body: "- Line 2 (`+var ward = 1`): call it warden\n"},
	)

	m := f.model()
	press(m, "a") // approve the original
	if m.cur < 0 || m.items[m.cur].Commit != fixup {
		t.Fatalf("on screen = %d, want the fixup", m.cur)
	}
	out := m.render()
	if !strings.Contains(out, "caused this fixup") || !strings.Contains(out, "call it warden") ||
		!strings.Contains(out, "fixup of "+original[:7]) {
		t.Errorf("fixup screen lacks its causes:\n%s", out)
	}
	press(m, "v")
	if !m.combined || !strings.Contains(m.render(), "combined view") {
		t.Fatal("v did not switch to the combined view")
	}
	var added []string
	for _, l := range m.combinedLines {
		if l.op == gitdiff.OpAdd {
			var b strings.Builder
			for _, c := range l.cells {
				b.WriteRune(c.r)
			}
			added = append(added, b.String())
		}
	}
	if len(added) != 1 || added[0] != "var warden = 1" {
		t.Errorf("combined additions = %q, want the fix folded into the original", added)
	}
	press(m, "a")
	if m.items[m.cur].Record != nil {
		t.Error("a decision was made from the combined view")
	}
	press(m, "v", "a")
	if m.cur != -1 {
		t.Error("approving the fixup in its own view did not finish the review")
	}
}

func TestNothingToReview(t *testing.T) {
	f := newFixture(t)
	m := f.model()
	if m.cur != -1 || !strings.Contains(m.render(), "Nothing left to review") {
		t.Errorf("empty review:\n%s", m.render())
	}
	press(m, "a", "u", "v", "j")
	m.Refresh()
}

func TestTaskLinesHang(t *testing.T) {
	m := &Model{width: 40}
	it := review.Item{}
	for i, title := range []string{"Catalog types and the shared helper", "Add census reporting",
		"Catalog the key and turn nodes", "Catalog the damage nodes", "Gate Effect"} {
		it.Tasks = append(it.Tasks, &queue.Task{ID: fmt.Sprintf("000%d", i+2), Title: title})
	}
	lines := m.taskLines(it)
	if len(lines) != taskLines || !strings.HasPrefix(lines[0], "task 0002") ||
		!strings.HasPrefix(lines[1], hangIndent) ||
		!strings.HasSuffix(lines[2], "…") {
		t.Errorf("task lines = %q", lines)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > m.width {
			t.Errorf("%q is wider than %d", l, m.width)
		}
	}
	if got := m.taskLines(review.Item{}); len(got) != 1 {
		t.Errorf("no tasks = %q", got)
	}
}
