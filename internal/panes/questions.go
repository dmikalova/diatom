package panes

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// questionRow is one open question in the questions pane, or a plan waiting
// for sign-off.
type questionRow struct {
	repo, goal string
	q          *queue.Question
	task       string
	// plan is set on a plan's row, with its goal's title in task.
	plan *plan.Plan
}

// id names a row across reloads.
func (r questionRow) id() string {
	if r.plan != nil {
		return r.goal + " plan " + r.plan.Summary
	}
	return r.goal + " " + r.q.ID
}

// Questions lists what the agents need from the human: the plans waiting for
// sign-off first, then the open questions of every goal in the repo, triage's
// included, so a question from a goal the human isn't looking at still
// reaches them (ADR 0009). It records their answers and sign-offs.
type Questions struct {
	ctx  context.Context
	env  Env
	rows []questionRow
	sel  int
	// seen holds the plans opened already; answering moves on to one not
	// yet seen before anything else.
	seen map[string]bool
	// confirm is the plan a first enter on an empty answer asked to sign off.
	confirm string
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

const (
	answerHint = "Your answer. enter sends it, shift+enter adds a line, esc goes back."
	planHint   = "enter with nothing typed signs the plan off. Or write what to change, and enter " +
		"sends it back to grilling. esc goes back."
)

// NewQuestions loads the questions pane.
func NewQuestions(ctx context.Context, env Env) *Questions {
	area := textarea.New()
	area.Placeholder = answerHint
	area.ShowLineNumbers = false
	area.Prompt = ""
	editKeys(&area)
	area.SetStyles(plainStyles())
	q := &Questions{ctx: ctx, env: env, area: area, seen: map[string]bool{}, width: 80, height: 24}
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
	var rows []questionRow
	for _, g := range goals {
		if g.State != queue.GoalDone && g.State != queue.GoalFinished {
			names = append(names, g.Name)
		}
		if g.State != queue.GoalPlanning {
			continue
		}
		p, err := plan.Load(store.GoalDir(g.Name))
		if err != nil {
			m.err = err
			continue
		}
		// A plan sent back waits for grilling, not the human.
		sent, err := intake.Pending(plan.FeedbackDir(store.GoalDir(g.Name)))
		if err != nil {
			m.err = err
			continue
		}
		if p != nil && len(sent) == 0 {
			rows = append(
				rows,
				questionRow{repo: store.Repo(), goal: g.Name, plan: p, task: g.Title},
			)
		}
	}
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
	keep := ""
	if m.sel < len(m.rows) {
		keep = m.rows[m.sel].id()
	}
	m.rows = rows
	for i, r := range rows {
		if r.id() == keep {
			m.sel = i
		}
	}
	m.sel = min(m.sel, max(len(rows)-1, 0))
}

// plans counts the plans waiting for sign-off.
func (m *Questions) plans() int {
	n := 0
	for _, r := range m.rows {
		if r.plan != nil {
			n++
		}
	}
	return n
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
	case tea.BlurMsg:
		m.area.Blur()
		return m, nil
	case tea.FocusMsg:
		if m.answering {
			return m, m.area.Focus()
		}
		return m, nil
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
		case "enter", "space", " ", "right", "l":
			if m.sel < len(m.rows) {
				m.open()
				return m, m.area.Focus()
			}
		case "r":
			m.reload()
		}
	default:
		// Pastes, the clipboard's replies and the cursor's blink are the
		// answer box's.
		if m.answering {
			var cmd tea.Cmd
			m.area, cmd = m.area.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// open shows the selected row in full, above the answer box.
func (m *Questions) open() {
	m.answering, m.scroll, m.confirm = true, 0, ""
	m.area.Placeholder = answerHint
	if r := m.rows[m.sel]; r.plan != nil {
		m.seen[r.id()] = true
		m.area.Placeholder = planHint
	}
}

func (m *Questions) updateAnswer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := cut(&m.area, msg); ok {
		return m, cmd
	}
	if msg.String() != "enter" {
		m.confirm = ""
	}
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
	case "down", "up":
		// While the answer is one line, the arrows, and a wheel sending
		// them, scroll the question; with more, they move through the answer.
		if m.area.LineCount() <= 1 {
			if msg.String() == "down" {
				m.scroll++
			} else {
				m.scroll = max(m.scroll-1, 0)
			}
			return m, nil
		}
	case "enter":
		text := strings.TrimSpace(m.area.Value())
		if m.sel >= len(m.rows) {
			return m, nil
		}
		row := m.rows[m.sel]
		if row.plan != nil {
			m.decide(row, text)
			return m, nil
		}
		if text == "" {
			return m, nil
		}
		if err := queue.Open(row.repo).Answer(row.goal, row.q.ID, text, m.env.Now()); err != nil {
			m.err = err
			return m, nil
		}
		m.area.Reset()
		m.flash = fmt.Sprintf("answered; task %s is ready again", row.q.Task)
		m.next()
		return m, nil
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

// decide signs a plan off on a second enter with nothing typed, or sends
// what was typed back to grilling as the changes wanted (ADR 0010).
func (m *Questions) decide(row questionRow, text string) {
	if text == "" {
		if m.confirm != row.id() {
			m.confirm = row.id()
			m.flash = fmt.Sprintf("press enter again to sign off %s: %d workstreams, %d tasks",
				row.goal, len(row.plan.Workstreams), len(row.plan.Tasks))
			return
		}
		cfg, err := config.Load(row.repo, m.env.Paths)
		if err == nil {
			err = plan.Approve(m.ctx, queue.Open(row.repo), cfg, row.goal, m.env.Now())
		}
		if err != nil {
			m.err = err
			return
		}
		m.flash = row.goal + " is signed off and active"
	} else {
		_, err := intake.Write(
			plan.FeedbackDir(queue.Open(row.repo).GoalDir(row.goal)),
			intake.Intake{Source: "questions", Created: m.env.Now(), Text: text},
		)
		if err != nil {
			m.err = err
			return
		}
		m.area.Reset()
		m.flash = "sent back to " + row.goal + "'s grilling with your changes"
	}
	m.next()
}

// next opens what comes after the row just answered: a plan not opened yet,
// since it holds a whole goal up, or else the row that leaving the list has
// moved into its place, or the first when it was the last. With none left,
// it goes back to the list.
func (m *Questions) next() {
	last := m.sel
	m.reload()
	m.scroll = 0
	if len(m.rows) == 0 {
		m.answering = false
		m.area.Blur()
		m.flash += "; nothing else waiting"
		return
	}
	m.sel = last
	if last >= len(m.rows) {
		m.sel = 0
	}
	for i, r := range m.rows {
		if r.plan != nil && !m.seen[r.id()] {
			m.sel = i
			break
		}
	}
	m.open()
}

// View implements tea.Model. The count is in the title, which zellij shows
// on the pane's frame, so the pane itself is only the questions.
func (m *Questions) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = fmt.Sprintf("questions · %d open", len(m.rows)-m.plans())
	switch n := m.plans(); n {
	case 0:
	case 1:
		v.WindowTitle += " · 1 plan to sign off"
	default:
		v.WindowTitle += fmt.Sprintf(" · %d plans to sign off", n)
	}
	v.ReportFocus = true
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
		if r.plan != nil {
			lines = append(lines, mark+tui.Color(oneLine(fmt.Sprintf(
				"plan to sign off: %d workstreams, %d tasks · %s",
				len(r.plan.Workstreams), len(r.plan.Tasks), r.task), m.width-4), tui.Green))
			continue
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
	var head, body string
	if r.plan != nil {
		head, body = tui.Dim(r.goal+" › plan to sign off ")+r.task, plan.Describe(r.plan)
	} else {
		head, body = tui.Dim(goalLabel(r.goal)+" › task "+r.q.Task+" ")+r.task, r.q.Text
	}
	text := strings.Split(ansi.Wordwrap(strings.TrimSpace(body), max(m.width, 20), ""), "\n")
	fit := max(room-m.area.Height()-3, 1)
	m.scroll = min(m.scroll, max(len(text)-fit, 0))
	shown := text[m.scroll:min(m.scroll+fit, len(text))]
	if m.scroll > 0 {
		shown = append([]string{tui.Dim("↑ more above")}, shown[1:]...)
	}
	if m.scroll+fit < len(text) {
		shown = append(shown[:len(shown)-1], tui.Dim("↓ more below"))
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
