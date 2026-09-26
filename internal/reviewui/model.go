// Package reviewui is diatom's native reviewer (ADR 0008): a terminal UI over
// the review record that shows each hunk still to review, with syntax colors
// and word-level changes, and records the human's decisions. The decisions
// go to the review store; the scheduler turns rejections into revisions.
package reviewui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/bluekeyes/go-gitdiff/gitdiff"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
)

// reloadEvery is how often the reviewer looks for new commits.
const reloadEvery = 5 * time.Second

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
	input   textinput.Model

	// combined shows a fixup folded into the commit it revises.
	combined      bool
	combinedLines []line

	// history is every decision, and back how far the human has stepped
	// back through it; back == len(history) is the present.
	history []review.Event
	back    int

	width, height int
	flash         string
	err           error
}

// New loads a goal's review and puts the first hunk to review on screen.
func New(ctx context.Context, s *queue.Store, goal string) (*Model, error) {
	in := textinput.New()
	in.Placeholder = "comment on this line; enter saves, esc cancels"
	m := &Model{
		ctx: ctx, store: s, goal: goal, rev: review.Store{Dir: s.GoalDir(goal)},
		repo: git.Repo{Dir: s.Repo()}, now: time.Now, input: in, cur: -1, width: 100, height: 30,
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

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(reloadEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(msg.Width-4, 10))
	case tickMsg:
		if !m.editing {
			m.err = m.reload()
			if m.cur < 0 {
				m.show(m.firstPending(""))
			}
		}
		return m, tick()
	case tea.KeyPressMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *Model) updateEditing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.input.Reset()
		return m, nil
	case "enter":
		if text := strings.TrimSpace(m.input.Value()); text != "" {
			m.drafts = slices.DeleteFunc(
				m.drafts,
				func(c review.Comment) bool { return c.Line == m.cursor },
			)
			m.drafts = append(m.drafts, review.Comment{Line: m.cursor, Text: text})
			slices.SortStableFunc(
				m.drafts,
				func(a, b review.Comment) int { return a.Line - b.Line },
			)
		}
		m.editing = false
		m.input.Reset()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.flash = ""
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "ctrl+d", "pgdown", "space":
		m.move(max(m.bodyHeight()/2, 1))
	case "ctrl+u", "pgup":
		m.move(-max(m.bodyHeight()/2, 1))
	case "c":
		if m.cur >= 0 && !m.combined {
			m.editing = true
			for _, c := range m.drafts {
				if c.Line == m.cursor {
					m.input.SetValue(c.Text)
				}
			}
			return m, m.input.Focus()
		}
	case "x":
		m.drafts = slices.DeleteFunc(
			m.drafts,
			func(c review.Comment) bool { return c.Line == m.cursor },
		)
	case "a":
		m.decide(review.Approve)
	case "r":
		m.decide(review.Reject)
	case "d":
		m.decide(review.Defer)
	case "n", "tab":
		m.skip(1)
	case "p", "shift+tab":
		m.skip(-1)
	case "u", "backspace":
		m.stepBack()
	case "v":
		m.toggleCombined()
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
	rec, err := m.rev.Decide(it.Hunk, d, m.drafts, m.now())
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
	if d == review.Approve && len(m.drafts) > 0 {
		m.flash += "; its comments went to intake"
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

// skip moves through the hunks left to review without deciding.
func (m *Model) skip(step int) {
	pending := review.Pending(m.items)
	if len(pending) == 0 {
		return
	}
	i := slices.IndexFunc(pending, func(it review.Item) bool { return key(it) == m.key() })
	i = ((i+step)%len(pending) + len(pending)) % len(pending)
	m.show(m.find(key(pending[i])))
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
