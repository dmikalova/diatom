package panes

import (
	"fmt"
	"strconv"
	"strings"

	keybind "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/tui"
)

// Intake takes free text for the repo (ADR 0009): new goals, playtest notes,
// anything. Triage sorts it into the repo's goals; the focused goal goes with
// it as a hint to where it belongs.
type Intake struct {
	env     Env
	area    textarea.Model
	focus   focus.Focus
	pending int

	width, height int
	flash         string
	err           error
}

// NewIntake loads the intake pane.
func NewIntake(env Env) *Intake {
	area := textarea.New()
	area.ShowLineNumbers = false
	area.Prompt = ""
	editKeys(&area)
	area.SetStyles(plainStyles())
	area.Focus()
	m := &Intake{env: env, area: area, width: 80, height: 12}
	m.reload()
	m.resize()
	return m
}

// plainStyles styles a text box in the terminal's own colors, as the rest of
// the panes are, rather than for a dark background: plain text, and the
// placeholder in the panes' gray.
func plainStyles() textarea.Styles {
	s := textarea.DefaultDarkStyles()
	plain := textarea.StyleState{
		Base:        lipgloss.NewStyle(),
		Text:        lipgloss.NewStyle(),
		CursorLine:  lipgloss.NewStyle(),
		EndOfBuffer: lipgloss.NewStyle(),
		Prompt:      lipgloss.NewStyle(),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(tui.Gray))),
		Selection:   lipgloss.NewStyle().Reverse(true),
	}
	s.Focused, s.Blurred = plain, plain
	return s
}

// The keys the text boxes edit with, beside the text box's own. The shifted
// ones need a terminal that tells them apart, as zellij and Ghostty do; the
// alt ones work in any. ctrl+g, the text box's own select-all, is zellij's
// lock.
var (
	// newline adds a line to the text instead of sending it.
	newline   = keybind.NewBinding(keybind.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	selectAll = keybind.NewBinding(keybind.WithKeys("alt+a", "ctrl+shift+a"))
	copyText  = keybind.NewBinding(keybind.WithKeys("ctrl+shift+c", "alt+c"))
	cutText   = keybind.NewBinding(keybind.WithKeys("ctrl+x", "alt+x"))
)

// editKeys gives a text box the panes' editing keys. Pasting, with the
// terminal's paste or ctrl+v, and selecting with shift and the arrows are the
// text box's own.
func editKeys(area *textarea.Model) {
	area.KeyMap.InsertNewline = newline
	area.KeyMap.SelectAll = selectAll
	area.KeyMap.CopySelection = copyText
}

// cut copies the selected text and deletes it, which the text box has no key
// for.
func cut(area *textarea.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if !keybind.Matches(msg, cutText) || !area.HasSelection() {
		return nil, false
	}
	cmd := area.CopySelection()
	area.DeleteSelection()
	return cmd, true
}

func (m *Intake) reload() {
	fc, err := m.env.Focus.Read()
	m.err = err
	m.focus = fc
	items, err := intake.Pending(intake.Dir(m.env.Store.Repo()))
	if err != nil {
		m.err = err
	}
	m.pending = len(items)
	m.area.Placeholder = m.placeholder()
}

// placeholder is what the empty pane says: what just happened, then how to
// use it. The pane is nothing but the text, so this is where hints go.
func (m *Intake) placeholder() string {
	var lines []string
	if m.flash != "" {
		lines = append(lines, m.flash, "")
	}
	lines = append(
		lines,
		"Tell the agents anything: a new goal, notes, a change of plan. Triage sorts it into the goals.",
	)
	about := "the repo"
	if m.focus.Goal != "" {
		about = "goal " + m.focus.Goal
	}
	lines = append(lines, "", "Looking at "+about+" · enter sends · shift+enter adds a line")
	if m.pending > 0 {
		lines = append(lines, fmt.Sprintf("%d waiting for triage", m.pending))
	}
	return strings.Join(lines, "\n")
}

// Init implements tea.Model.
func (m *Intake) Init() tea.Cmd { return tea.Batch(tick(), textarea.Blink) }

// Update implements tea.Model.
func (m *Intake) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case tickMsg:
		m.reload()
		return m, tick()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			m.submit()
			return m, nil
		}
		m.flash = ""
		if cmd, ok := cut(&m.area, msg); ok {
			return m, cmd
		}
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

// resize fills the pane with the text, less a line for an error.
func (m *Intake) resize() {
	h := m.height
	if m.err != nil {
		h--
	}
	m.area.SetWidth(max(m.width, 10))
	m.area.SetHeight(max(h, 2))
}

func (m *Intake) submit() {
	text := strings.TrimSpace(m.area.Value())
	if text == "" {
		return
	}
	m.reload()
	if _, err := intake.Write(intake.Dir(m.env.Store.Repo()), intake.Intake{
		Source: "pane", Created: m.env.Now(), Goal: m.focus.Goal, Text: text,
	}); err != nil {
		m.err = err
		m.resize()
		return
	}
	m.area.Reset()
	m.flash = "Sent for triage."
	m.reload()
}

// View implements tea.Model.
func (m *Intake) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Intake) render() string {
	out := m.area.View()
	if m.err != nil {
		out += "\n" + tui.Color(m.err.Error(), tui.Red)
	}
	return out
}

// Editing reports whether anything is typed and not yet queued.
func (m *Intake) Editing() bool { return strings.TrimSpace(m.area.Value()) != "" }
