package panes

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// questionRow is one open question in the questions pane.
type questionRow struct {
	repo, goal string
	q          *queue.Question
	task       string
}

// Questions lists the open questions of every goal in every repo, so a
// question from a goal the human isn't looking at still reaches them
// (ADR 0009), and records their answers.
type Questions struct {
	env       Env
	rows      []questionRow
	sel       int
	answering bool
	area      textarea.Model

	width, height int
	flash         string
	err           error
}

// NewQuestions loads the questions pane.
func NewQuestions(env Env) *Questions {
	area := textarea.New()
	area.Placeholder = "your answer; ctrl+s or alt+enter saves, esc cancels"
	area.ShowLineNumbers = false
	q := &Questions{env: env, area: area, width: 80, height: 24}
	q.reload()
	return q
}

func (m *Questions) reload() {
	m.err = nil
	repos, err := m.env.Registry.List()
	if err != nil {
		m.err = err
		return
	}
	var rows []questionRow
	for _, repo := range repos {
		store := queue.Open(repo)
		goals, err := store.Goals()
		if err != nil {
			m.err = err
			continue
		}
		for _, g := range goals {
			if g.State == queue.GoalDone || g.State == queue.GoalFinished {
				continue
			}
			qs, err := store.Questions(g.Name, queue.QuestionOpen)
			if err != nil {
				m.err = err
				continue
			}
			for _, q := range qs {
				if q.Answer != "" {
					continue
				}
				row := questionRow{repo: repo, goal: g.Name, q: q}
				if t, err := store.Task(g.Name, q.Task); err == nil {
					row.task = t.Title
				}
				rows = append(rows, row)
			}
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
		m.area.SetWidth(max(msg.Width-2, 10))
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
		case "enter", "a":
			if m.sel < len(m.rows) {
				m.answering = true
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
	case "ctrl+s", "alt+enter":
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

// View implements tea.Model.
func (m *Questions) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Questions) render() string {
	var b strings.Builder
	b.WriteString(tui.Bold("questions") + tui.Dim(fmt.Sprintf(" · %d open", len(m.rows))) + "\n\n")
	if len(m.rows) == 0 {
		b.WriteString(
			tui.Dim(
				"No questions. The agents ask here when they need you to decide something.",
			) + "\n",
		)
	}
	for i, r := range m.rows {
		mark := "  "
		if i == m.sel {
			mark = tui.Color("› ", tui.Cyan)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(r.q.Text), "\n")
		if len(first) > max(m.width-30, 20) {
			first = first[:max(m.width-30, 20)] + "…"
		}
		fmt.Fprintf(&b, "%s%s %s\n", mark, tui.Dim(filepath.Base(r.repo)+"/"+r.goal), first)
	}
	if m.sel < len(m.rows) {
		r := m.rows[m.sel]
		b.WriteString("\n" + tui.Dim(strings.Repeat("─", max(m.width, 1))) + "\n")
		fmt.Fprintf(
			&b,
			"%s %s\n\n%s\n",
			tui.Color("task "+r.q.Task, tui.Magenta),
			r.task,
			strings.TrimSpace(r.q.Text),
		)
	}
	if m.answering {
		b.WriteString("\n" + m.area.View() + "\n")
	}
	if m.err != nil {
		b.WriteString("\n" + tui.Color(m.err.Error(), tui.Red) + "\n")
	}
	if m.flash != "" {
		b.WriteString("\n" + tui.Color(m.flash, tui.Cyan) + "\n")
	}
	if !m.answering {
		b.WriteString("\n" + tui.Dim("enter answer · j/k move · q quit"))
	}
	return b.String()
}
