package panes

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// questionRow is one open question in the questions pane.
type questionRow struct {
	repo, goal string
	q          *queue.Question
	task       string
}

// Questions lists the open questions of every goal in the repo, triage's
// included, so a question from a goal the human isn't looking at still
// reaches them (ADR 0009), and records their answers.
type Questions struct {
	env  Env
	rows []questionRow
	sel  int
	// top is the first line of the list on screen, which scrolls to keep
	// the selected question in view.
	top int
	// answering opens the selected question to answer it; scroll is how far
	// down its text is shown.
	answering bool
	scroll    int
	area      textarea.Model

	width, height int
	flash         string
	err           error
}

// NewQuestions loads the questions pane.
func NewQuestions(env Env) *Questions {
	area := textarea.New()
	area.Placeholder = "Your answer. enter sends it, shift+enter adds a line, esc goes back."
	area.ShowLineNumbers = false
	area.Prompt = ""
	area.KeyMap.InsertNewline = newline
	area.SetStyles(plainStyles())
	q := &Questions{env: env, area: area, width: 80, height: 24}
	q.reload()
	return q
}

func (m *Questions) reload() {
	m.err = nil
	store := m.env.Store
	goals, err := store.Goals()
	if err != nil {
		m.err = err
		return
	}
	names := []string{queue.IntakeGoal}
	for _, g := range goals {
		if g.State != queue.GoalDone && g.State != queue.GoalFinished {
			names = append(names, g.Name)
		}
	}
	var rows []questionRow
	for _, name := range names {
		qs, err := store.Questions(name, queue.QuestionOpen)
		if err != nil {
			m.err = err
			continue
		}
		for _, q := range qs {
			if q.Answer != "" {
				continue
			}
			row := questionRow{repo: store.Repo(), goal: name, q: q}
			if t, err := store.Task(name, q.Task); err == nil {
				row.task = t.Title
			}
			rows = append(rows, row)
		}
	}
	m.rows = rows
	m.sel = min(m.sel, max(len(rows)-1, 0))
}

// Init implements tea.Model.
func (m *Questions) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (m *Questions) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.area.SetWidth(max(msg.Width, 10))
		m.area.SetHeight(max(msg.Height/3, 3))
	case tickMsg:
		if !m.answering {
			m.reload()
		}
		return m, tick()
	case tea.KeyPressMsg:
		if m.answering {
			return m.updateAnswer(msg)
		}
		m.flash = ""
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.sel = min(m.sel+1, max(len(m.rows)-1, 0))
		case "k", "up":
			m.sel = max(m.sel-1, 0)
		case "enter", "right", "l":
			if m.sel < len(m.rows) {
				m.answering, m.scroll = true, 0
				return m, m.area.Focus()
			}
		case "r":
			m.reload()
		}
	}
	return m, nil
}

func (m *Questions) updateAnswer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.answering = false
		m.area.Blur()
		return m, nil
	case "pgdown":
		m.scroll++
		return m, nil
	case "pgup":
		m.scroll = max(m.scroll-1, 0)
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.area.Value())
		if text == "" || m.sel >= len(m.rows) {
			return m, nil
		}
		row := m.rows[m.sel]
		if err := queue.Open(row.repo).Answer(row.goal, row.q.ID, text, m.env.Now()); err != nil {
			m.err = err
			return m, nil
		}
		m.answering = false
		m.area.Reset()
		m.area.Blur()
		m.flash = fmt.Sprintf("answered; task %s is ready again", row.q.Task)
		m.reload()
		return m, nil
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

// View implements tea.Model. The count is in the title, which zellij shows
// on the pane's frame, so the pane itself is only the questions.
func (m *Questions) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = fmt.Sprintf("questions · %d open", len(m.rows))
	if m.answering {
		v.WindowTitle += " · enter sends · shift+enter adds a line · esc back"
	} else if len(m.rows) > 0 {
		v.WindowTitle += " · enter answers"
	}
	return v
}

func (m *Questions) render() string {
	var foot []string
	if m.err != nil {
		foot = append(foot, tui.Color(m.err.Error(), tui.Red))
	}
	if m.flash != "" {
		foot = append(foot, tui.Color(m.flash, tui.Cyan))
	}
	room := max(m.height-len(foot), 3)
	var body string
	if m.answering && m.sel < len(m.rows) {
		body = m.renderQuestion(m.rows[m.sel], room)
	} else {
		body = m.renderList(room)
	}
	return strings.Join(append([]string{body}, foot...), "\n")
}

// renderList lists the questions under their goals, scrolled to keep the
// selected one in view.
func (m *Questions) renderList(room int) string {
	if len(m.rows) == 0 {
		return tui.Dim("No questions. The agents ask here when they need you to decide something.")
	}
	var lines []string
	selLine := 0
	for i, r := range m.rows {
		if i == 0 || r.goal != m.rows[i-1].goal {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, tui.Bold(goalLabel(r.goal)))
		}
		mark := "  "
		if i == m.sel {
			mark = tui.Color("› ", tui.Cyan)
			selLine = len(lines)
		}
		lines = append(lines, mark+oneLine(r.q.Text, m.width-4))
	}
	// Scroll just enough to show the selected question, with its goal's
	// name above it when that fits.
	switch {
	case selLine < m.top+1:
		m.top = max(selLine-1, 0)
	case selLine >= m.top+room:
		m.top = selLine - room + 1
	}
	m.top = min(m.top, max(len(lines)-room, 0))
	return strings.Join(lines[m.top:min(m.top+room, len(lines))], "\n")
}

// renderQuestion shows one question in full, wrapped to the pane, above the
// answer.
func (m *Questions) renderQuestion(r questionRow, room int) string {
	head := tui.Dim(goalLabel(r.goal)+" › task "+r.q.Task+" ") + r.task
	text := strings.Split(ansi.Wordwrap(strings.TrimSpace(r.q.Text), max(m.width, 20), ""), "\n")
	fit := max(room-m.area.Height()-3, 1)
	m.scroll = min(m.scroll, max(len(text)-fit, 0))
	shown := text[m.scroll:min(m.scroll+fit, len(text))]
	if m.scroll+fit < len(text) {
		shown = append(shown[:len(shown)-1], tui.Dim("… pgdn for more"))
	}
	return head + "\n\n" + strings.Join(shown, "\n") + "\n\n" + m.area.View()
}

// Editing reports whether an answer is being written.
func (m *Questions) Editing() bool { return m.answering }

// goalLabel names a question's goal: triage's questions are about intake.
func goalLabel(goal string) string {
	if goal == queue.IntakeGoal {
		return "intake"
	}
	return goal
}
