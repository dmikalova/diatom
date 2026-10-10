package reviewui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/tui"
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
	f := &fixture{t: t, repo: r, store: queue.At(r.Dir, t.TempDir(), "github.com/me/toy")}
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
		case "shift+enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
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

	// A comment on the cursor line rejects the hunk with it, and moves on.
	first := m.items[m.cur].Hunk
	press(m, "c")
	typeText(m, "call it ward2")
	press(m, "enter")
	if !strings.Contains(m.flash, "revises it with your comment") {
		t.Errorf("flash = %q", m.flash)
	}
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
	press(m, "b")
	if m.cur < 0 || m.items[m.cur].Record.Decision != review.Approve {
		t.Fatalf("step back = %d", m.cur)
	}
	press(m, "d")
	if m.cur < 0 || m.items[m.cur].Record.Decision != review.Defer ||
		!strings.Contains(m.render(), "(1 deferred)") {
		t.Errorf("a deferred hunk that is the only one left should stay on screen:\n%s", m.render())
	}
	press(m, "b", "b")
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
	if len(m.drafts) != 0 || m.editing || m.items[m.cur].Record != nil {
		t.Error("esc kept the comment")
	}
	// An empty comment decides nothing.
	press(m, "c", "enter")
	if m.editing || m.items[m.cur].Record != nil {
		t.Error("an empty comment rejected the hunk")
	}
	// Stepping back to a rejected hunk shows its comment, and approving it
	// drops the comment rather than sending it anywhere.
	press(m, "c")
	typeText(m, "keep")
	press(m, "enter", "b")
	if len(m.drafts) != 1 || !strings.Contains(ansi.Strip(m.render()), "💬 keep") {
		t.Fatalf("stepped back to drafts %+v", m.drafts)
	}
	press(m, "a")
	rec, _ := m.rev.Load(sha)
	for _, r := range rec.Hunks {
		if r.Decision != review.Approve || len(r.Comments) != 0 {
			t.Errorf("approved with %+v", r)
		}
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
		!strings.HasPrefix(lines[1], tui.HangIndent) ||
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

func TestHunkHeadWraps(t *testing.T) {
	m := &Model{width: 40, height: 30}
	it := review.Item{Commit: "b64fe6b0000", Path: "b/.agents/skills/implement-cards/SKILL.md",
		Subject: "Merge remote-tracking branch 'origin/main' into diatom/strip/ws/retire",
		Index:   1, Of: 2}
	head := m.hunkHead(it)
	if len(head) < 4 || !strings.HasPrefix(ansi.Strip(head[1]), tui.HangIndent) {
		t.Fatalf("head = %q", head)
	}
	for _, l := range head {
		if ansi.StringWidth(l) > m.width {
			t.Errorf("%q is wider than %d", l, m.width)
		}
	}
	m.items, m.cur = []review.Item{it}, 0
	if got := m.bodyHeight(); got != 30-headerLines-len(head)-footerLines {
		t.Errorf("body = %d with a head of %d", got, len(head))
	}
}

func TestImagesBeforeAndAfter(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	f := newFixture(t)
	gem := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="%s"/></svg>`
	f.write("gem.svg", fmt.Sprintf(gem, "#f00"))
	f.commit("feat: a gem")
	f.write("gem.svg", fmt.Sprintf(gem, "#00f"))
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	f.write("dot.png", buf.String())
	sha := f.commit("feat: a blue gem and a dot")
	f.task(
		&queue.Task{Title: "Draw", Kind: queue.Planned, Workstream: "art", Commits: []string{sha}},
	)
	m := f.model()
	at := func(path string) {
		t.Helper()
		for i, it := range m.items {
			if it.Path == path {
				m.show(i)
				return
			}
		}
		t.Fatalf("no hunk of %s in %d", path, len(m.items))
	}
	at("dot.png")
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "none: the file is new") || !strings.Contains(out, "after · 3×2") ||
		!strings.Contains(
			out,
			"A binary file: approve or reject",
		) || !strings.ContainsRune(out, '\U0010EEEE') {
		t.Errorf("the png:\n%s", out)
	}
	if seq := m.Images(); !strings.Contains(seq, "\x1b_Ga=T,U=1") {
		t.Errorf("the png wasn't sent: %q", seq)
	}
	if seq := m.Images(); seq != "" {
		t.Error("the png was sent twice")
	}
	at("gem.svg")
	if out := ansi.Strip(m.render()); !strings.Contains(out, "before · 512×512") ||
		!strings.Contains(out, "after · 512×512") || !strings.Contains(out, "+ <svg") {
		t.Errorf("the svg:\n%s", out)
	}
	if seq := m.Images(); strings.Count(seq, "a=T") != 2 {
		t.Errorf("the svg sent %d images", strings.Count(seq, "a=T"))
	}
	// A file git no longer has shows why.
	if d := m.decode("nope", "gone.png"); d.err == nil {
		t.Error("a missing file decoded")
	}
	// A terminal without images shows none.
	m.pics.on = false
	if m.preview(m.items[m.cur]) != nil {
		t.Error("a preview without images")
	}
}

func TestCommentBoxGrowsAndTheFooterStays(t *testing.T) {
	f := newFixture(t)
	f.write("ward.go", body("ward", "u"))
	f.task(&queue.Task{Title: "Add ward", Kind: queue.Planned, Workstream: "engine",
		Commits: []string{f.commit("feat: ward")}})
	m := f.model()
	if foot := ansi.Strip(
		m.footer(),
	); !strings.Contains(foot, "c comment · r reject · d defer · b back") ||
		strings.Contains(foot, "combined") ||
		strings.Contains(foot, "skip") {
		t.Errorf("footer = %q", foot)
	}
	press(m, "c")
	typeText(m, "first")
	for range 14 {
		press(m, "shift+enter")
		typeText(m, "more")
	}
	if m.input.Height() != commentLines || !strings.Contains(ansi.Strip(m.footer()),
		"enter sends the comment to the goal's agent · shift+enter adds a line") {
		t.Errorf("box height %d, footer %q", m.input.Height(), ansi.Strip(m.footer()))
	}
	if lines := strings.Split(strings.TrimRight(m.render(), "\n"), "\n"); len(lines) > m.height {
		t.Errorf("editing drew %d lines in %d", len(lines), m.height)
	}
	press(m, "enter", "b")
	if len(m.drafts) != 1 || !strings.HasPrefix(m.drafts[0].Text, "first\nmore") {
		t.Fatalf("drafts = %+v", m.drafts)
	}
	// A long comment wraps inside the reviewer, and the keys stay on screen.
	m.drafts[0].Text = strings.Repeat("this needs another look ", 12)
	out := strings.Split(strings.TrimRight(ansi.Strip(m.render()), "\n"), "\n")
	if len(out) > m.height || !strings.Contains(out[len(out)-1], "a approve") {
		t.Errorf("%d lines in %d, last %q", len(out), m.height, out[len(out)-1])
	}
}

// TestOpenInEditor pins that o opens the hunk's file at the line under the
// cursor, from the worktree of the workstream that made it.
func TestOpenInEditor(t *testing.T) {
	f := newFixture(t)
	f.write("ward.go", body("ward", "poison"))
	sha := f.commit("feat: ward and poison")
	f.task(&queue.Task{Title: "Add ward", Kind: queue.Planned, Workstream: "engine",
		Commits: []string{sha}})
	m := f.model()
	if m.editCommand() != nil || strings.Contains(m.footer(), "o open") {
		t.Error("o works with no editor set")
	}
	m.Editor = []string{"nvim", "-R"}
	if !strings.Contains(m.footer(), "o open") {
		t.Errorf("footer = %q", m.footer())
	}
	cmd := m.editCommand()
	line := m.items[m.cur].NewLine(m.cursor)
	if cmd == nil ||
		!slices.Equal(cmd.Args, []string{"nvim", "-R", fmt.Sprintf("+%d", line), "ward.go"}) ||
		cmd.Dir != f.repo.Dir {
		t.Fatalf("without a worktree: %+v", cmd)
	}
	wt := f.store.WorktreeDir("set", "engine")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if cmd := m.editCommand(); cmd.Dir != wt {
		t.Errorf("opened in %s, want the workstream's worktree %s", cmd.Dir, wt)
	}
	// VS Code takes the line its own way, and keeps the --goto it was given.
	for _, editor := range [][]string{{"code"}, {"code", "--goto"}} {
		m.Editor = editor
		want := append(slices.Clone(editor), "--goto", fmt.Sprintf("ward.go:%d", line))
		if len(editor) > 1 {
			want = append(slices.Clone(editor), fmt.Sprintf("ward.go:%d", line))
		}
		if cmd := m.editCommand(); !slices.Equal(cmd.Args, want) {
			t.Errorf("%v opens with %v, want %v", editor, cmd.Args, want)
		}
	}
}

// TestCommentBoxTakesCmdKeys pins that the comment box takes the
// terminal's paste, as cmd+v sends it, and cmd+a and cmd+x.
func TestCommentBoxTakesCmdKeys(t *testing.T) {
	f := newFixture(t)
	f.write("ward.go", body("ward", "poison"))
	sha := f.commit("feat: ward and poison")
	f.task(&queue.Task{Title: "Add ward", Kind: queue.Planned, Workstream: "engine",
		Commits: []string{sha}})
	m := f.model()
	press(m, "c")
	m.Update(tea.PasteMsg{Content: "why ward?"})
	if m.input.Value() != "why ward?" {
		t.Fatalf("after a paste, the box holds %q", m.input.Value())
	}
	m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModSuper})
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModSuper})
	if m.input.Value() != "" {
		t.Errorf("after cmd+a, cmd+x, the box holds %q", m.input.Value())
	}
}
