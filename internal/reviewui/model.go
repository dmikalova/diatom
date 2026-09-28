// Package reviewui is diatom's native reviewer (ADR 0008): a terminal UI over
// the review record that shows each hunk still to review, with syntax colors
// and word-level changes, and records the human's decisions. The decisions
// go to the review store; the scheduler turns rejections into revisions.
package reviewui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/termimg"
	"github.com/dmikalova/diatom/internal/tui"
)

// Model is the reviewer's state.
type Model struct {
	ctx   context.Context
	store *queue.Store
	goal  string
	rev   review.Store
	repo  git.Repo
	now   func() time.Time

	items []review.Item
	// cur is the index in items of the hunk on screen, or -1 for none.
	cur    int
	lines  []line
	cursor int
	scroll int
	drafts []review.Comment

	editing bool
	input   textarea.Model

	// combined shows a fixup folded into the commit it revises.
	combined      bool
	combinedLines []line

	// history is every decision, and back how far the human has stepped
	// back through it; back == len(history) is the present.
	history []review.Event
	back    int

	// pics are the images an image file's hunks show.
	pics images

	width, height int
	flash         string
	err           error

	// Editor is the command o opens the hunk's file in, such as nvim, run in
	// the worktree of the workstream that made it; empty turns o off.
	Editor []string
}

// New loads a goal's review and puts the first hunk to review on screen.
func New(ctx context.Context, s *queue.Store, goal string) (*Model, error) {
	in := tui.TextBox()
	in.Placeholder = "What to change about this line"
	m := &Model{
		ctx: ctx, store: s, goal: goal, rev: review.Store{Dir: s.GoalDir(goal)},
		repo: git.Repo{Dir: s.Repo()}, now: time.Now, input: in, cur: -1, width: 100, height: 30,
		pics: images{
			on: termimg.Enabled(
				os.Getenv,
			),
			sent:    map[string]picture{},
			decoded: map[string]decoded{},
		},
	}
	if err := m.reload(); err != nil {
		return nil, err
	}
	m.show(m.firstPending(""))
	return m, nil
}

// reload rereads the review, keeping the same hunk on screen.
func (m *Model) reload() error {
	keep := m.key()
	items, err := review.Load(m.ctx, m.store, m.goal)
	if err != nil {
		return err
	}
	history, err := m.rev.History()
	if err != nil {
		return err
	}
	atPresent := m.back == len(m.history)
	m.items, m.history = items, history
	if atPresent {
		m.back = len(history)
	}
	if keep != "" {
		m.cur = m.find(keep)
		if m.cur >= 0 {
			m.lines = lines(m.items[m.cur].Hunk)
		}
	}
	return nil
}

func key(it review.Item) string { return it.Commit + ":" + it.ID }

func (m *Model) key() string {
	if m.cur < 0 || m.cur >= len(m.items) {
		return ""
	}
	return key(m.items[m.cur])
}

func (m *Model) find(k string) int {
	return slices.IndexFunc(m.items, func(it review.Item) bool { return key(it) == k })
}

// firstPending returns the first hunk left to review other than skip, or
// skip itself when it is the only one, or -1.
func (m *Model) firstPending(skip string) int {
	pending := review.Pending(m.items)
	for _, it := range pending {
		if key(it) != skip {
			return m.find(key(it))
		}
	}
	if len(pending) > 0 {
		return m.find(key(pending[0]))
	}
	return -1
}

// show puts the item at index i on screen.
func (m *Model) show(i int) {
	m.cur, m.cursor, m.scroll, m.combined, m.drafts = i, 0, 0, false, nil
	m.lines = nil
	if i < 0 {
		return
	}
	it := m.items[i]
	m.lines = lines(it.Hunk)
	if it.Record != nil {
		m.drafts = slices.Clone(it.Record.Comments)
	}
	m.cursor = firstChange(m.lines)
}

func firstChange(ls []line) int {
	for i, l := range ls {
		if l.op != gitdiff.OpContext {
			return i
		}
	}
	return 0
}

// Update handles the window's messages: its size, and keys.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *Model) updateEditing(msg tea.KeyPressMsg) (*Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.input.Reset()
		return m, nil
	case "enter":
		// A comment goes back to the goal's agent, recorded as a rejection:
		// the agent acts on it, and the next hunk comes up.
		text := strings.TrimSpace(m.input.Value())
		m.editing = false
		m.input.Reset()
		if text == "" {
			return m, nil
		}
		m.drafts = slices.DeleteFunc(
			m.drafts,
			func(c review.Comment) bool { return c.Line == m.cursor },
		)
		m.drafts = append(m.drafts, review.Comment{Line: m.cursor, Text: text})
		slices.SortStableFunc(
			m.drafts,
			func(a, b review.Comment) int { return a.Line - b.Line },
		)
		m.decide(review.Reject)
		return m, nil
	}
	if cmd, ok := tui.Cut(&m.input, msg); ok {
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.fitInput()
	return m, cmd
}

// commentLines is the most lines the comment box grows to.
const commentLines = 10

// fitInput grows the comment box with its text, up to commentLines.
func (m *Model) fitInput() {
	w := max(m.width-4, 10)
	m.input.SetWidth(w)
	text := strings.TrimSuffix(m.input.Value(), "\n")
	n := len(strings.Split(ansi.Wordwrap(text, w, ""), "\n"))
	if strings.HasSuffix(m.input.Value(), "\n") {
		n++
	}
	m.input.SetHeight(min(max(n, 1), commentLines))
}

func (m *Model) updateKey(msg tea.KeyPressMsg) (*Model, tea.Cmd) {
	m.flash = ""
	switch msg.String() {
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "ctrl+d", "pgdown", "space":
		m.move(max(m.bodyHeight()/2, 1))
	case "ctrl+u", "pgup", "shift+space":
		m.move(-max(m.bodyHeight()/2, 1))
	case "c":
		if m.cur >= 0 && !m.combined {
			m.editing = true
			for _, c := range m.drafts {
				if c.Line == m.cursor {
					m.input.SetValue(c.Text)
				}
			}
			m.fitInput()
			return m, m.input.Focus()
		}
	case "a":
		m.decide(review.Approve)
	case "r":
		m.decide(review.Reject)
	case "d":
		m.decide(review.Defer)
	case "b", "backspace":
		m.stepBack()
	case "v":
		m.toggleCombined()
	case "o":
		return m, m.edit()
	case "R":
		m.err = m.reload()
	}
	return m, nil
}

// decide records a decision on the hunk on screen and moves on to the next
// one left to review.
func (m *Model) decide(d review.Decision) {
	if m.cur < 0 || m.combined {
		return
	}
	it := m.items[m.cur]
	comments := m.drafts
	if d == review.Approve {
		// Approving says the hunk is fine as it is: comments ask for a
		// change, which only a rejection makes.
		comments = nil
	}
	rec, err := m.rev.Decide(it.Hunk, d, comments, m.now())
	if err != nil {
		m.err = err
		return
	}
	m.items[m.cur].Record = rec
	m.history = append(
		m.history,
		review.Event{Commit: it.Commit, Hunk: it.ID, Decision: d, At: rec.At},
	)
	m.back = len(m.history)
	m.flash = fmt.Sprintf("%s %s", pastTense(d), it.ID)
	if d == review.Reject && len(comments) > 0 {
		m.flash += "; the goal's agent revises it with your comment"
	}
	m.show(m.firstPending(key(it)))
	if m.cur >= 0 && key(m.items[m.cur]) == key(it) && d != review.Defer {
		m.show(-1)
	}
}

func pastTense(d review.Decision) string {
	switch d {
	case review.Approve:
		return "approved"
	case review.Reject:
		return "rejected"
	}
	return "deferred"
}

// stepBack moves to the hunk of the decision before the last one shown, so a
// hunk approved on autopilot can be decided again (ADR 0001).
func (m *Model) stepBack() {
	for m.back > 0 {
		m.back--
		e := m.history[m.back]
		if i := m.find(e.Commit + ":" + e.Hunk); i >= 0 && i != m.cur {
			m.show(i)
			m.flash = fmt.Sprintf("stepped back to a hunk you %s", pastTense(e.Decision))
			return
		}
	}
	m.flash = "no earlier decisions"
}

// toggleCombined switches a fixup between its own diff and the commit it
// revises with the fix folded in, as it will look after autosquash.
func (m *Model) toggleCombined() {
	if m.cur < 0 {
		return
	}
	it := m.items[m.cur]
	if it.Revision == nil || it.Revision.Revises == "" {
		m.flash = "only a fixup has a combined view"
		return
	}
	if m.combined {
		m.combined, m.cursor, m.scroll = false, firstChange(m.lines), 0
		return
	}
	hunks, err := review.Combined(m.ctx, m.repo, it.Revision.Revises, it.Commit)
	if err != nil {
		m.err = err
		return
	}
	m.combinedLines = nil
	for _, h := range hunks {
		if h.Path == it.Path {
			m.combinedLines = append(m.combinedLines, lines(h)...)
		}
	}
	m.combined, m.cursor, m.scroll = true, 0, 0
}

func (m *Model) shown() []line {
	if m.combined {
		return m.combinedLines
	}
	return m.lines
}

func (m *Model) move(step int) {
	n := len(m.shown())
	if n == 0 {
		return
	}
	m.cursor = min(max(m.cursor+step, 0), n-1)
	h := m.bodyHeight()
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+h {
		m.scroll = m.cursor - h + 1
	}
}

// Editing reports whether a comment is being written, which a restart would
// lose.
func (m *Model) Editing() bool { return m.editing }

// EditedMsg is sent once the editor o opened has exited, with why it
// failed, if it did.
type EditedMsg struct{ Err error }

// edit opens the hunk's file in the editor, at the line under the cursor,
// from the worktree of the workstream that made it, so the editor sees the
// code as that work has it. The window gives the editor the terminal and
// takes it back once the editor exits.
func (m *Model) edit() tea.Cmd {
	cmd := m.editCommand()
	if cmd == nil {
		return nil
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return EditedMsg{Err: err} })
}

// editCommand is the editor's command for the hunk on screen, nil for none.
func (m *Model) editCommand() *exec.Cmd {
	if m.cur < 0 || len(m.Editor) == 0 {
		return nil
	}
	it := m.items[m.cur]
	dir := m.store.Repo()
	for _, t := range it.Tasks {
		if wt := m.store.WorktreeDir(m.goal, t.Workstream); t.Workstream != "" && isDir(wt) {
			dir = wt
			break
		}
	}
	args := slices.Clone(m.Editor[1:])
	if !it.Deleted {
		if line := it.NewLine(m.cursor); line > 0 {
			args = append(args, fmt.Sprintf("+%d", line))
		}
		args = append(args, filepath.FromSlash(it.Path))
	}
	cmd := exec.CommandContext(m.ctx, m.Editor[0], args...)
	cmd.Dir = dir
	return cmd
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
