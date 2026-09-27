package panes

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// navWidth is how wide the nav is.
const navWidth = 32

// Scheduler is the scheduler an App runs in its own process.
type Scheduler struct {
	// Viewer says why this app runs none, such as diatom being open on the
	// repo elsewhere; "" when it runs one.
	Viewer string
	// Stop suspends the running sessions, and Done is closed once they are
	// suspended and the scheduler has stopped.
	Stop func()
	Done <-chan struct{}
}

// QuitMsg asks the app to suspend its sessions and quit, as a signal does.
type QuitMsg struct{}

// stoppedMsg says the scheduler has stopped.
type stoppedMsg struct{}

// part is one of the app's parts that takes the keyboard, in the order tab
// moves through them.
type part int

const (
	partNav part = iota
	partMain
	partIntake
	parts
)

// entryKind is what a line of the nav opens.
type entryKind int

const (
	entryNext entryKind = iota
	entryIntake
	entryGoal
	entryFinished
	entryFinishedGoal
)

// entry is one line of the nav.
type entry struct {
	kind entryKind
	// row is the goal's, or the intake's; nil for the intake before anything
	// is sent.
	row *goalRow
	// goal is a finished goal's.
	goal *queue.Goal
}

func (e entry) key() string {
	switch {
	case e.row != nil:
		return e.row.goal.Name
	case e.goal != nil:
		return e.goal.Name
	}
	return "entry " + strconv.Itoa(int(e.kind))
}

// App is diatom in one window (ADR 0007): the nav down the left, with Next,
// the intake and the repo's goals, the intake box at its foot, and the main
// pane showing what the nav has selected. It runs the scheduler too, and
// quitting suspends the sessions it runs.
type App struct {
	ctx   context.Context
	env   Env
	sched Scheduler

	status    *Status
	questions *Questions
	intake    *Intake

	// sel is the nav line selected, and shown the entry the main pane shows.
	sel   int
	shown string
	focus part
	// finished are the goals landed upstream, listed under their fold when
	// it is open.
	finished []*queue.Goal
	unfolded bool
	// quitting is set once the sessions are suspending, and atOnce when the
	// human quit without waiting for them.
	quitting, atOnce bool

	width, height int
}

// NewApp loads the app.
func NewApp(ctx context.Context, env Env, sched Scheduler) *App {
	a := &App{
		ctx: ctx, env: env, sched: sched,
		status:    NewStatus(ctx, env),
		questions: NewQuestions(ctx, env),
		intake:    NewIntake(env),
		width:     120, height: 40,
	}
	a.intake.compact = true
	a.intake.area.Blur()
	a.reload()
	a.show()
	return a
}

func (a *App) reload() {
	a.status.reload()
	if !a.questions.answering {
		a.questions.reload()
	}
	a.intake.reload()
	a.finished = nil
	if goals, err := a.env.Store.Goals(); err == nil {
		for _, g := range goals {
			if g.State == queue.GoalFinished {
				a.finished = append(a.finished, g)
			}
		}
	}
	a.sel = min(a.sel, len(a.entries())-1)
}

// entries are the nav's lines: Next, the intake, the goals being worked on,
// and the finished ones under a fold.
func (a *App) entries() []entry {
	es := []entry{{kind: entryNext}, {kind: entryIntake}}
	for i := range a.status.rows {
		r := &a.status.rows[i]
		if r.intake {
			es[1].row = r
			continue
		}
		es = append(es, entry{kind: entryGoal, row: r})
	}
	if len(a.finished) > 0 {
		es = append(es, entry{kind: entryFinished})
		if a.unfolded {
			for _, g := range a.finished {
				es = append(es, entry{kind: entryFinishedGoal, goal: g})
			}
		}
	}
	return es
}

func (a *App) selected() entry {
	es := a.entries()
	return es[max(min(a.sel, len(es)-1), 0)]
}

// show points the main pane at the selected entry, when it isn't already.
func (a *App) show() {
	e := a.selected()
	if e.key() == a.shown {
		return
	}
	a.shown = e.key()
	a.status.detail = nil
	if e.row != nil {
		a.status.openDetail(e.row)
		if !e.row.intake {
			// The zellij panes still follow the focus.
			_ = a.env.Focus.Write(focus.Focus{Goal: e.row.goal.Name})
		}
	}
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.layout()
	case tickMsg:
		a.reload()
		a.show()
		return a, tick()
	case jobMsg:
		_, cmd := a.status.Update(msg)
		return a, cmd
	case QuitMsg:
		return a.quit(false)
	case stoppedMsg:
		return a, tea.Quit
	case tea.KeyPressMsg:
		return a.key(msg)
	case tea.MouseClickMsg:
		return a.click(msg.Mouse())
	case tea.MouseWheelMsg:
		return a.wheel(msg.Mouse())
	case tea.PasteMsg:
		return a.toFocused(msg)
	default:
		// The text boxes' own messages, such as the cursor's blink.
		var c1, c2 tea.Cmd
		a.intake.area, c1 = a.intake.area.Update(msg)
		a.questions.area, c2 = a.questions.area.Update(msg)
		return a, tea.Batch(c1, c2)
	}
	return a, nil
}

// layout sizes the parts to the window.
func (a *App) layout() {
	mw, h := a.mainWidth(), a.height
	a.status.width, a.status.height = mw, h
	a.questions.width, a.questions.height = mw, h
	a.questions.area.SetWidth(max(mw, 10))
	a.questions.area.SetHeight(max(h/3, 3))
	a.intake.width, a.intake.height = navWidth, a.intakeHeight()
	a.intake.resize()
}

func (a *App) mainWidth() int { return max(a.width-navWidth-1, 20) }

// intakeHeight is the intake box's: a line, and more while it is typed in.
func (a *App) intakeHeight() int {
	if a.focus != partIntake {
		return 1
	}
	return min(max(a.intake.area.LineCount()+1, 3), max(a.height/4, 3))
}

// typing reports whether a text box has the keyboard, so letters are text.
func (a *App) typing() bool {
	switch a.focus {
	case partIntake:
		return true
	case partMain:
		return a.selected().kind == entryNext && a.questions.answering
	}
	return false
}

func (a *App) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return a.quit(a.quitting)
	}
	if a.quitting {
		return a, nil
	}
	switch key {
	case "tab":
		return a, a.setFocus((a.focus + 1) % parts)
	case "shift+tab":
		return a, a.setFocus((a.focus + parts - 1) % parts)
	}
	if !a.typing() {
		switch key {
		case "q":
			return a.quit(false)
		case "i":
			return a, a.setFocus(partIntake)
		}
	}
	switch a.focus {
	case partNav:
		return a.navKey(key)
	case partIntake:
		if key == "esc" {
			return a, a.setFocus(partNav)
		}
		_, cmd := a.intake.Update(msg)
		a.layout()
		return a, cmd
	}
	return a.mainKey(msg)
}

func (a *App) navKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		a.sel = min(a.sel+1, len(a.entries())-1)
	case "k", "up":
		a.sel = max(a.sel-1, 0)
	case "enter", "space", " ", "right", "l":
		if a.selected().kind == entryFinished {
			a.unfolded = !a.unfolded
			return a, nil
		}
		return a, a.setFocus(partMain)
	}
	a.show()
	return a, nil
}

// mainKey hands a key to what the main pane shows. Going back from its top
// level moves to the nav.
func (a *App) mainKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	back := key == "esc" || key == "left"
	e := a.selected()
	switch e.kind {
	case entryNext:
		if back && !a.questions.answering {
			return a, a.setFocus(partNav)
		}
		_, cmd := a.questions.Update(msg)
		return a, cmd
	case entryIntake, entryGoal:
		if a.status.detail == nil || back && a.status.detail.task == nil {
			return a, a.setFocus(partNav)
		}
		_, cmd := a.status.updateDetail(msg)
		if a.status.detail == nil {
			// Its own way back, such as h, reached the top.
			a.shown = ""
			a.show()
			return a, tea.Batch(cmd, a.setFocus(partNav))
		}
		return a, cmd
	}
	if back {
		return a, a.setFocus(partNav)
	}
	return a, nil
}

// toFocused hands a message to the text box with the keyboard.
func (a *App) toFocused(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch {
	case a.focus == partIntake:
		a.intake.area, cmd = a.intake.area.Update(msg)
		a.layout()
	case a.typing():
		a.questions.area, cmd = a.questions.area.Update(msg)
	}
	return a, cmd
}

// setFocus gives part the keyboard, and the cursor to its text box.
func (a *App) setFocus(p part) tea.Cmd {
	a.focus = p
	a.intake.area.Blur()
	a.questions.area.Blur()
	var cmd tea.Cmd
	switch {
	case p == partIntake:
		cmd = a.intake.area.Focus()
	case a.typing():
		cmd = a.questions.area.Focus()
	}
	a.layout()
	return cmd
}

// quit suspends the running sessions and quits once they are, or at once
// when now is set or the app runs no scheduler.
func (a *App) quit(now bool) (tea.Model, tea.Cmd) {
	if now || a.sched.Stop == nil {
		a.atOnce = now && a.sched.Stop != nil
		return a, tea.Quit
	}
	if !a.quitting {
		a.quitting = true
		a.sched.Stop()
	}
	done := a.sched.Done
	return a, func() tea.Msg {
		<-done
		return stoppedMsg{}
	}
}

// StoppedAtOnce reports whether the human quit without waiting for the
// sessions to suspend.
func (a *App) StoppedAtOnce() bool { return a.atOnce }

// click selects the nav line clicked, or gives the keyboard to the part
// clicked.
func (a *App) click(m tea.Mouse) (tea.Model, tea.Cmd) {
	if m.Button != tea.MouseLeft || a.quitting {
		return a, nil
	}
	if m.X >= navWidth {
		return a, a.setFocus(partMain)
	}
	lines, _ := a.navLines()
	if m.Y >= a.height-a.intakeHeight() {
		return a, a.setFocus(partIntake)
	}
	if m.Y < len(lines) && lines[m.Y].entry >= 0 {
		a.sel = lines[m.Y].entry
		if a.selected().kind == entryFinished {
			a.unfolded = !a.unfolded
		}
		a.show()
	}
	return a, a.setFocus(partNav)
}

// wheel scrolls what is under the pointer, as its arrow keys do.
func (a *App) wheel(m tea.Mouse) (tea.Model, tea.Cmd) {
	var code rune
	switch m.Button {
	case tea.MouseWheelDown:
		code = tea.KeyDown
	case tea.MouseWheelUp:
		code = tea.KeyUp
	default:
		return a, nil
	}
	key := tea.KeyPressMsg{Code: code}
	if m.X < navWidth {
		return a.navKey(key.String())
	}
	if a.selected().kind == entryNext && !a.questions.answering {
		return a, nil
	}
	return a.mainKey(key)
}

// View implements tea.Model.
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	total := 0.0
	for _, r := range a.status.rows {
		total += r.cost
	}
	v.WindowTitle = fmt.Sprintf("diatom · %s · $%.2f", filepath.Base(a.env.Store.Repo()), total)
	return v
}

func (a *App) render() string {
	nav := lipgloss.NewStyle().Width(navWidth).MaxWidth(navWidth).Height(a.height).
		MaxHeight(a.height).Render(a.renderNav())
	sep := tui.Dim("│")
	if a.focus == partMain {
		sep = tui.Color("│", tui.Cyan)
	}
	border := strings.TrimSuffix(strings.Repeat(sep+"\n", a.height), "\n")
	mw := a.mainWidth()
	main := lipgloss.NewStyle().Width(mw).MaxWidth(mw).Height(a.height).MaxHeight(a.height).
		Render(a.renderMain())
	return lipgloss.JoinHorizontal(lipgloss.Top, nav, border, main)
}

// navLine is one line of the nav, and the entry it selects, or -1.
type navLine struct {
	text  string
	entry int
}

// navLines are the nav's entries, and the lines under them: its footer and
// the intake box.
func (a *App) navLines() ([]navLine, []string) {
	var lines []navLine
	for i, e := range a.entries() {
		if e.kind == entryGoal && (i == 0 || a.entries()[i-1].kind != entryGoal) {
			lines = append(lines, navLine{text: "", entry: -1})
		}
		lines = append(lines, navLine{text: a.navEntry(i, e), entry: i})
	}
	var foot []string
	for l := range strings.SplitSeq(ansi.Wordwrap(a.footer(), navWidth-1, ""), "\n") {
		foot = append(foot, " "+l)
	}
	label := "─ intake "
	label += strings.Repeat("─", navWidth-ansi.StringWidth(label))
	if a.focus == partIntake {
		label = tui.Color(label, tui.Cyan)
	} else {
		label = tui.Dim(label)
	}
	foot = append(foot, label)
	return lines, foot
}

func (a *App) renderNav() string {
	lines, foot := a.navLines()
	box := strings.Split(strings.TrimRight(a.intake.render(), "\n"), "\n")
	room := max(a.height-len(foot)-len(box), 1)
	var out []string
	for _, l := range lines {
		out = append(out, l.text)
	}
	if len(out) > room {
		out = out[:room]
	}
	for len(out) < room {
		out = append(out, "")
	}
	return strings.Join(append(append(out, foot...), box...), "\n")
}

// navEntry renders one entry: a glyph for where it stands, its name cut to
// fit, and what waits on the human.
func (a *App) navEntry(i int, e entry) string {
	var glyph, name, badges string
	switch e.kind {
	case entryNext:
		glyph, name = tui.Color("»", tui.Cyan), "Next"
		if n := len(a.questions.rows); n > 0 {
			badges = tui.Color(strconv.Itoa(n), tui.Magenta)
		}
	case entryIntake:
		glyph, name = tui.Color("+", tui.Blue), "Intake"
		if e.row != nil {
			badges = a.badges(*e.row)
		}
	case entryGoal:
		glyph, name, badges = navGlyph(*e.row), e.row.goal.Title, a.badges(*e.row)
		if name == "" {
			name = e.row.goal.Name
		}
	case entryFinished:
		glyph, name = tui.Dim("▸"), fmt.Sprintf("Finished (%d)", len(a.finished))
		if a.unfolded {
			glyph = tui.Dim("▾")
		}
	case entryFinishedGoal:
		glyph, name = tui.Dim(" ✓"), e.goal.Title
	}
	room := navWidth - 4 - ansi.StringWidth(badges)
	if badges != "" {
		room--
	}
	name = ansi.Truncate(name, max(room, 4), "…")
	pad := strings.Repeat(" ", max(room-ansi.StringWidth(name), 0))
	if badges != "" {
		pad += " "
	}
	line := " " + glyph + " " + name + pad + badges
	if i == a.sel {
		if a.focus == partNav {
			return tui.SGR(7) + ansi.Strip(line) + tui.Reset
		}
		return tui.Bold(line)
	}
	return line
}

// badges counts what waits on the human in a goal: questions, and hunks to
// review.
func (a *App) badges(r goalRow) string {
	var b []string
	if r.questions > 0 {
		b = append(b, tui.Color(fmt.Sprintf("?%d", r.questions), tui.Magenta))
	}
	if r.toReview > 0 {
		b = append(b, tui.Color(fmt.Sprintf("±%d", r.toReview), tui.Yellow))
	}
	return strings.Join(b, " ")
}

// navGlyph is where a goal stands, as the nav shows it.
func navGlyph(r goalRow) string {
	name, c := goalStatus(r)
	g, ok := map[string]string{
		"active": "▶", "queued": "◷", "blocked": "⊘", "reviewing": "✎", "ready to finish": "✓",
		"planning": "◌", "parked": "‖", "done": "✓",
	}[name]
	if !ok {
		g = "·"
	}
	return tui.Color(g, c)
}

// footer is the repo's health: the scheduler, what runs, and what it cost.
func (a *App) footer() string {
	total, running := 0.0, 0
	for _, r := range a.status.rows {
		total += r.cost
		running += len(r.activeWork)
	}
	parts := []string{fmt.Sprintf("$%.2f", total), fmt.Sprintf("%d running", running)}
	switch {
	case a.quitting:
		parts = append(parts, tui.Color("suspending…", tui.Yellow))
	case a.sched.Viewer != "":
		parts = append(parts, tui.Color("viewer: "+a.sched.Viewer, tui.Yellow))
	case a.status.health != "":
		parts = append(parts, tui.Color("⚠ "+a.status.health, tui.Red))
	}
	return strings.Join(parts, tui.Dim(" · "))
}

func (a *App) renderMain() string {
	if a.quitting {
		return "Suspending the running sessions; they carry on where they stopped the next time " +
			"diatom opens.\n\n" + tui.Dim("ctrl+c again stops at once, leaving any git work half done.")
	}
	e := a.selected()
	switch e.kind {
	case entryNext:
		return a.questions.render()
	case entryIntake:
		if e.row == nil {
			return tui.Dim("Nothing sent yet. Write in the intake box below the nav, and triage " +
				"sorts it into goals.")
		}
		return a.status.renderDetail()
	case entryGoal:
		if a.status.detail == nil {
			return ""
		}
		return a.status.renderDetail()
	case entryFinished:
		return tui.Dim(fmt.Sprintf("%d goals are finished and landed upstream. enter lists them.",
			len(a.finished)))
	}
	g := e.goal
	out := tui.Bold(g.Title) + "\n" + tui.Dim(g.Name+" · finished")
	if g.Description != "" {
		out += "\n\n" + g.Description
	}
	return out
}
