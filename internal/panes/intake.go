package panes

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/tui"
)

// Intake takes free text for new work (ADR 0009): a new goal, playtest notes,
// anything. It goes to the focused goal, or to the focused repo when no goal
// is focused, and waits there for triage.
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
	area.Placeholder = "new work, notes, a new goal… ctrl+s or alt+enter queues it for triage"
	area.ShowLineNumbers = false
	area.Focus()
	m := &Intake{env: env, area: area, width: 80, height: 12}
	m.reload()
	return m
}

func (m *Intake) reload() {
	fc, err := m.env.Focus.Read()
	m.err = err
	m.focus = fc
	m.pending = 0
	if fc.Repo == "" {
		return
	}
	items, err := intake.Pending(intake.Dir(fc.Repo, fc.Goal))
	if err != nil {
		m.err = err
	}
	m.pending = len(items)
}

// Init implements tea.Model.
func (m *Intake) Init() tea.Cmd { return tea.Batch(tick(), textarea.Blink) }

// Update implements tea.Model.
func (m *Intake) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.area.SetWidth(max(msg.Width-2, 10))
		m.area.SetHeight(max(msg.Height-5, 2))
	case tickMsg:
		m.reload()
		return m, tick()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+s", "alt+enter":
			m.submit()
			return m, nil
		}
		m.flash = ""
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

func (m *Intake) submit() {
	text := strings.TrimSpace(m.area.Value())
	if text == "" {
		return
	}
	m.reload()
	if m.focus.Repo == "" {
		m.flash = "nothing is focused: pick a repo or goal in the status pane first"
		return
	}
	if _, err := intake.Write(intake.Dir(m.focus.Repo, m.focus.Goal), intake.Intake{
		Source: "pane", Created: m.env.Now(), Text: text,
	}); err != nil {
		m.err = err
		return
	}
	m.area.Reset()
	m.flash = "queued for triage in " + describe(m.focus)
	m.reload()
}

// View implements tea.Model.
func (m *Intake) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Intake) render() string {
	var b strings.Builder
	b.WriteString(tui.Bold("intake") + tui.Dim(" → "+describe(m.focus)))
	if m.pending > 0 {
		b.WriteString(tui.Dim(fmt.Sprintf(" · %d waiting for triage", m.pending)))
	}
	b.WriteString("\n" + m.area.View() + "\n")
	switch {
	case m.err != nil:
		b.WriteString(tui.Color(m.err.Error(), tui.Red))
	case m.flash != "":
		b.WriteString(tui.Color(m.flash, tui.Cyan))
	default:
		b.WriteString(tui.Dim("ctrl+s queue · ctrl+c quit"))
	}
	return b.String()
}
