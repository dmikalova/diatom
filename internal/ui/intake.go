package ui

import (
	"fmt"
	"strconv"
	"strings"

	keybind "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/tui"
)

// Intake is the box at the foot of the nav, which takes free text for the
// repo (ADR 0009): new goals, playtest notes, anything. Triage sorts it into
// the repo's goals, with what the main pane showed as a clue to where it
// belongs.
type Intake struct {
	env     Env
	area    textarea.Model
	pending int
	// about is what the main pane shows: the goal, and a clue for triage.
	about func() (goal, context string)

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
	m := &Intake{env: env, area: area, width: 80, height: 1}
	m.reload()
	m.resize()
	return m
}

// plainStyles styles a text box in the terminal's own colors, as the rest of
// the window is, rather than for a dark background: plain text, and the
// placeholder in the window's gray.
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
	s.Cursor.Color = lipgloss.Color(strconv.Itoa(tui.Accent))
	return s
}

// The keys the text boxes edit with, beside the text box's own. The shifted
// ones need a terminal that tells them apart, as Ghostty does; the alt ones
// work in any.
var (
	// newline adds a line to the text instead of sending it.
	newline   = keybind.NewBinding(keybind.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	selectAll = keybind.NewBinding(keybind.WithKeys("alt+a", "ctrl+shift+a"))
	copyText  = keybind.NewBinding(keybind.WithKeys("ctrl+shift+c", "alt+c"))
	cutText   = keybind.NewBinding(keybind.WithKeys("ctrl+x", "alt+x"))
)

// editKeys gives a text box the window's editing keys. Pasting, with the
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
	items, err := intake.Pending(intake.Dir(m.env.Store.Repo()))
	m.err = err
	m.pending = len(items)
	m.area.Placeholder = m.placeholder()
}

// placeholder is what the empty box says: what just happened, or how to use
// it.
func (m *Intake) placeholder() string {
	switch {
	case m.flash != "":
		return m.flash
	case m.pending > 0:
		return fmt.Sprintf("%d waiting for triage · tell diatom anything", m.pending)
	}
	return "Tell diatom anything · i"
}

// key handles a key while the box has the keyboard: enter sends.
func (m *Intake) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "enter" {
		m.submit()
		return nil
	}
	m.flash = ""
	if cmd, ok := cut(&m.area, msg); ok {
		return cmd
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return cmd
}

// resize fills the box with the text, less a line for an error.
func (m *Intake) resize() {
	h := m.height
	if m.err != nil {
		h--
	}
	m.area.SetWidth(max(m.width, 10))
	m.area.SetHeight(max(h, 1))
}

func (m *Intake) submit() {
	text := strings.TrimSpace(m.area.Value())
	if text == "" {
		return
	}
	m.reload()
	in := intake.Intake{Source: "window", Created: m.env.Now(), Text: text}
	if m.about != nil {
		in.Goal, in.Context = m.about()
	}
	if _, err := intake.Write(intake.Dir(m.env.Store.Repo()), in); err != nil {
		m.err = err
		m.resize()
		return
	}
	m.area.Reset()
	m.flash = "Sent for triage."
	m.reload()
}

func (m *Intake) render() string {
	out := m.area.View()
	if m.err != nil {
		out += "\n" + tui.Color(m.err.Error(), tui.Red)
	}
	return out
}
