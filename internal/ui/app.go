package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/reviewui"
	"github.com/dmikalova/diatom/internal/tui"
)

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

// UpdateMsg says a newer diatom is installed at Bin, and Why: a release, or
// a build installed over this one. U restarts on it.
type UpdateMsg struct{ Bin, Why string }

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

	status *Status
	next   *Next
	intake *Intake

	// sel is the nav line selected, and shown the entry the main pane shows;
	// navTop is the nav's first line on screen.
	sel, navTop int
	shown       string
	focus       part
	// fromNext is set while a goal opened from Next's context is shown:
	// going back from it returns to Next.
	fromNext bool
	// finished are the goals landed upstream, listed under their fold when
	// it is open.
	finished []*queue.Goal
	unfolded bool
	// quitting is set once the sessions are suspending, and atOnce when the
	// human quit without waiting for them.
	quitting, atOnce bool
	// review is the review of a goal opened from its page.
	review *reviewui.Model
	// navW is the nav's width, which dragging its edge changes, and
	// navHidden hides it; dragging is set while the edge is held.
	navW                int
	navHidden, dragging bool
	// blurred is set while the terminal doesn't have the keyboard, and
	// waiting is how many items Next had at the last reload: when some
	// arrive while the human is elsewhere, the terminal tells them.
	blurred bool
	waiting int
	// logOpen shows the scheduler's log in the main pane, scrolled back
	// logBack lines.
	logOpen bool
	logBack int
	// spin is the wheel events waiting to scroll; frame is the window as last
	// drawn, and dirty says it has changed since.
	spin  spin
	frame string
	dirty bool
	// update is a newer diatom, and restart the one to run once quit.
	update  *UpdateMsg
	restart string
	flash   string

	width, height int
}

// NewApp loads the app.
func NewApp(ctx context.Context, env Env, sched Scheduler) *App {
	status := NewStatus(ctx, env)
	a := &App{
		ctx: ctx, env: env, sched: sched,
		status: status,
		next:   NewNext(ctx, env, status),
		intake: NewIntake(env),
		width:  120, height: 40,
	}
	a.intake.about = a.onScreen
	a.intake.area.Blur()
	status.openReview = a.openReview
	a.loadUI()
	a.reload()
	a.waiting = len(a.next.items)
	a.show()
	return a
}

// openReview shows a goal's review in place of its page.
func (a *App) openReview(goal string) {
	rv, err := reviewui.New(a.ctx, a.env.Store, goal)
	if err != nil {
		a.status.err = err
		return
	}
	a.review = rv
	a.layout()
}

// clearNotices drops what the last action came to, once the human does
// something else: a key, a click, or moving to another view. Until then it
// stays on screen, however often the window reloads.
func (a *App) clearNotices() {
	a.flash = ""
	a.status.flash, a.status.err = "", nil
	a.next.flash, a.next.err = "", nil
}

// Restart is the diatom to run once the app has quit, "" for none.
func (a *App) Restart() string { return a.restart }

func (a *App) reload() {
	a.status.reload()
	a.next.reload()
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
	a.status.detail, a.review, a.logOpen = nil, nil, false
	a.clearNotices()
	if e.row != nil {
		a.status.openDetail(e.row)
	}
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd { return tea.Batch(tick(), tea.RequestBackgroundColor) }

// Update implements tea.Model. Bubble Tea draws the window after every
// message, so wheel events only gather, to scroll once a frame, and a message
// that changes nothing is drawn from the last frame.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(tea.MouseWheelMsg); ok {
		return a, a.gather(m.Mouse())
	}
	a.dirty = true
	return a.handle(msg)
}

func (a *App) handle(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case wheelMsg:
		a.spinOut()
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.layout()
	case tickMsg:
		a.reload()
		a.show()
		if a.review != nil {
			a.review.Refresh()
		}
		return a, tea.Batch(tick(), a.notify())
	case tea.BackgroundColorMsg:
		// The reviewer tints the diff to suit the background.
		reviewui.SetDark(msg.IsDark())
	case tea.FocusMsg:
		a.blurred = false
	case tea.BlurMsg:
		a.blurred = true
	case UpdateMsg:
		a.update = &msg
	case jobMsg:
		a.status.jobDone(msg)
		a.next.reload()
	case QuitMsg:
		return a.quit(false)
	case stoppedMsg:
		return a, tea.Quit
	case tea.KeyPressMsg:
		return a.key(msg)
	case tea.MouseClickMsg:
		return a.click(msg.Mouse())
	case tea.MouseMotionMsg:
		if a.dragging {
			a.drag(msg.Mouse().X)
		}
	case tea.MouseReleaseMsg:
		if a.dragging {
			a.dragging = false
			a.saveUI()
		}
	case tea.PasteMsg:
		return a.toFocused(msg)
	default:
		// The text boxes' own messages, such as the cursor's blink.
		var c1, c2 tea.Cmd
		a.intake.area, c1 = a.intake.area.Update(msg)
		a.next.answer, c2 = a.next.answer.Update(msg)
		return a, tea.Batch(c1, c2)
	}
	return a, nil
}

// layout sizes the parts to the window.
func (a *App) layout() {
	mw, h := a.mainWidth(), a.height
	a.status.width, a.status.height = mw, h
	a.next.width, a.next.height = mw, h
	a.intake.width, a.intake.height = a.nw(), a.intakeHeight()
	a.intake.resize()
}

func (a *App) mainWidth() int {
	if a.nw() == 0 {
		return max(a.width, 20)
	}
	return max(a.width-a.nw()-1, 20)
}

// intakeHeight is the intake box's: a line, and as many as its text takes
// while it is typed in, up to half the nav.
func (a *App) intakeHeight() int {
	if a.focus != partIntake {
		return 1
	}
	text := strings.TrimSuffix(a.intake.area.Value(), "\n")
	lines := len(strings.Split(ansi.Wordwrap(text, max(a.nw()-1, 1), ""), "\n"))
	if strings.HasSuffix(a.intake.area.Value(), "\n") {
		lines++
	}
	return min(max(lines, 1), max(a.height/2, 1))
}

// typing reports whether a text box has the keyboard, so letters are text.
func (a *App) typing() bool {
	switch a.focus {
	case partIntake:
		return true
	case partMain:
		if a.review != nil {
			return a.review.Editing()
		}
		return a.selected().kind == entryNext && a.next.typing()
	}
	return false
}

func (a *App) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	a.clearNotices()
	switch key {
	case "ctrl+c":
		return a.quit(a.quitting)
	case "super+c":
		return a, a.copyFocused()
	}
	if a.quitting {
		return a, nil
	}
	switch key {
	case "tab":
		return a, a.tab(1)
	case "shift+tab":
		return a, a.tab(-1)
	}
	if !a.typing() {
		switch key {
		case "q":
			return a.quit(false)
		case "i":
			return a, a.setFocus(partIntake)
		case "h":
			a.toggleNav()
			return a, nil
		case "y":
			return a, a.copyFocused()
		case "L":
			a.logOpen, a.logBack = !a.logOpen, 0
			if a.logOpen {
				return a, a.setFocus(partMain)
			}
			return a, nil
		case "U":
			if a.update != nil {
				a.restart = a.update.Bin
				return a.quit(false)
			}
		}
	}
	switch a.focus {
	case partNav:
		return a.navKey(key)
	case partIntake:
		if key == "esc" {
			return a, a.setFocus(partNav)
		}
		cmd := a.intake.key(msg)
		a.layout()
		return a, cmd
	}
	return a.mainKey(msg)
}

func (a *App) navKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		a.sel = min(a.sel+1, len(a.entries())-1)
		a.fromNext = false
	case "k", "up":
		a.sel = max(a.sel-1, 0)
		a.fromNext = false
	case "enter", "space", " ", "right", "l":
		switch a.selected().kind {
		case entryFinished:
			a.unfolded = !a.unfolded
			return a, nil
		case entryNext:
			// An item opens on the item itself, to read before answering.
			return a, a.focusMain(int(areaBody))
		}
		return a, a.setFocus(partMain)
	}
	a.show()
	return a, nil
}

// tab moves the keyboard forward through the nav, the main pane's areas and
// the intake box, or back with a negative step.
func (a *App) tab(step int) tea.Cmd {
	areas := a.mainAreas()
	switch a.focus {
	case partNav:
		if step > 0 {
			return a.focusMain(0)
		}
		return a.setFocus(partIntake)
	case partIntake:
		if step > 0 {
			return a.setFocus(partNav)
		}
		return a.focusMain(areas - 1)
	}
	if i := a.mainArea() + step; i >= 0 && i < areas {
		return a.focusMain(i)
	}
	if step > 0 {
		return a.setFocus(partIntake)
	}
	return a.setFocus(partNav)
}

// mainAreas is how many areas of the main pane take the keyboard, and
// mainArea the one that has it.
func (a *App) mainAreas() int {
	if a.selected().kind == entryNext && !a.logOpen {
		return a.next.areas()
	}
	return 1
}

func (a *App) mainArea() int {
	if a.selected().kind == entryNext && !a.logOpen {
		return int(a.next.area)
	}
	return 0
}

// focusMain gives the keyboard to one of the main pane's areas.
func (a *App) focusMain(i int) tea.Cmd {
	if a.focus == partIntake {
		a.intake.flash = ""
		a.intake.reload()
	}
	a.focus = partMain
	a.intake.area.Blur()
	var cmd tea.Cmd
	if a.selected().kind == entryNext {
		cmd = a.next.setArea(nextArea(i))
	}
	a.layout()
	return cmd
}

// openFromNext shows the goal of Next's item, and going back from it returns
// to Next.
func (a *App) openFromNext(goal string) tea.Cmd {
	for i, e := range a.entries() {
		if e.row != nil && e.row.goal.Name == goal {
			a.sel, a.fromNext = i, true
			a.show()
			return a.setFocus(partMain)
		}
	}
	return nil
}

// backToNext returns from a goal opened from Next.
func (a *App) backToNext() tea.Cmd {
	a.sel, a.fromNext = 0, false
	a.show()
	return a.focusMain(int(areaBody))
}

// mainKey hands a key to what the main pane shows. Going back from its top
// level moves to the nav.
func (a *App) mainKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	back := key == "esc" || key == "left"
	if a.logOpen {
		a.logKey(key)
		if !a.logOpen {
			return a, a.setFocus(partNav)
		}
		return a, nil
	}
	if rv := a.review; rv != nil {
		if back && !rv.Editing() {
			// Back to the goal's page, its hunks counted again.
			a.review = nil
			a.status.reload()
			a.next.reload()
			return a, nil
		}
		return a, rv.Key(msg)
	}
	e := a.selected()
	switch e.kind {
	case entryNext:
		res := a.next.key(msg)
		switch {
		case res.back:
			return a, a.setFocus(partNav)
		case res.open != "":
			return a, a.openFromNext(res.open)
		}
		// Next reloads itself after whatever changes what waits, so a key
		// that only moves or scrolls costs nothing more.
		return a, res.cmd
	case entryIntake, entryGoal:
		if a.status.detail == nil || back && a.status.detail.task == nil {
			if a.fromNext {
				return a, a.backToNext()
			}
			return a, a.setFocus(partNav)
		}
		cmd := a.status.updateDetail(msg)
		if a.status.detail == nil {
			// Its own way back, such as h, reached the top.
			a.shown = ""
			a.show()
			if a.fromNext {
				return a, tea.Batch(cmd, a.backToNext())
			}
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
	case a.typing() && a.review == nil:
		a.next.answer, cmd = a.next.answer.Update(msg)
	}
	return a, cmd
}

// setFocus gives part the keyboard, and the cursor to its text box.
func (a *App) setFocus(p part) tea.Cmd {
	if p == partMain {
		return a.focusMain(a.mainArea())
	}
	if a.navHidden {
		// Going to the nav, or the intake box at its foot, shows it again.
		a.navHidden = false
		a.saveUI()
	}
	if a.focus == partIntake && p != partIntake {
		// What sending said is old news once the box is left.
		a.intake.flash = ""
		a.intake.reload()
	}
	a.focus = p
	a.intake.area.Blur()
	a.next.answer.Blur()
	var cmd tea.Cmd
	if p == partIntake {
		cmd = a.intake.area.Focus()
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
	a.clearNotices()
	nw := a.nw()
	if nw > 0 && m.X == nw {
		// The nav's edge, held to drag it.
		a.dragging = true
		return a, nil
	}
	if m.X > nw || nw == 0 {
		if a.selected().kind == entryNext && !a.logOpen {
			return a, a.focusMain(int(a.next.areaAt(m.Y)))
		}
		return a, a.setFocus(partMain)
	}
	lines, foot := a.navLines()
	if m.Y >= a.height-a.intakeHeight() {
		return a, a.setFocus(partIntake)
	}
	if m.Y >= a.height-a.intakeHeight()-len(foot) {
		// The footer opens the log.
		a.logOpen, a.logBack = true, 0
		return a, a.setFocus(partMain)
	}
	if y := m.Y + a.navTop; y < len(lines) && lines[y].entry >= 0 {
		a.sel = lines[y].entry
		if a.selected().kind == entryFinished {
			a.unfolded = !a.unfolded
		}
		a.show()
	}
	return a, a.setFocus(partNav)
}

// wheelFrame is how long wheel events gather before they scroll: a fast spin
// scrolls once a frame, by all of it, rather than once an event.
const wheelFrame = 16 * time.Millisecond

// wheelMsg scrolls by the wheel events gathered.
type wheelMsg struct{}

// spin is the wheel events gathered since the last scroll: over the nav or
// the main pane, and how many steps, down positive.
type spin struct {
	nav       bool
	steps     int
	scheduled bool
}

// gather adds a wheel event to the spin, scrolling at the end of the frame.
func (a *App) gather(m tea.Mouse) tea.Cmd {
	step := 0
	switch m.Button {
	case tea.MouseWheelDown:
		step = 1
	case tea.MouseWheelUp:
		step = -1
	default:
		return nil
	}
	nav := m.X < a.nw()
	if a.spin.steps != 0 && a.spin.nav != nav {
		// The pointer moved to the other part: what went before scrolls first.
		a.spinOut()
		a.dirty = true
	}
	a.spin.nav = nav
	a.spin.steps += step
	if a.spin.scheduled {
		return nil
	}
	a.spin.scheduled = true
	return tea.Tick(wheelFrame, func(time.Time) tea.Msg { return wheelMsg{} })
}

// spinOut scrolls what was under the pointer by the steps gathered, as its
// arrow keys do.
func (a *App) spinOut() {
	steps, nav := a.spin.steps, a.spin.nav
	a.spin = spin{}
	if steps == 0 {
		return
	}
	code := tea.KeyDown
	if steps < 0 {
		code = tea.KeyUp
	}
	key := tea.KeyPressMsg{Code: code}
	n := max(steps, -steps)
	if nav {
		a.sel = max(min(a.sel+steps, len(a.entries())-1), 0)
		a.fromNext = false
		a.show()
		return
	}
	if it := a.next.shown(); a.selected().kind == entryNext && !a.logOpen && it != nil &&
		it.kind != itemReview {
		a.next.scrollBy(3 * steps)
		return
	}
	for range n {
		a.mainKey(key)
	}
}

// View implements tea.Model, drawing again only after a change.
func (a *App) View() tea.View {
	if a.dirty || a.frame == "" {
		a.frame, a.dirty = a.render(), false
	}
	v := tea.NewView(a.frame)
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
	mw := a.mainWidth()
	main := lipgloss.NewStyle().Width(mw).MaxWidth(mw).Height(a.height).MaxHeight(a.height).
		Render(a.renderMain())
	nw := a.nw()
	if nw == 0 {
		return main
	}
	nav := lipgloss.NewStyle().Width(nw).MaxWidth(nw).Height(a.height).
		MaxHeight(a.height).Render(a.renderNav())
	sep := tui.Dim("│")
	if a.focus == partMain || a.dragging {
		sep = tui.Color("│", tui.Cyan)
	}
	border := strings.TrimSuffix(strings.Repeat(sep+"\n", a.height), "\n")
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
		for _, l := range a.navEntry(i, e) {
			lines = append(lines, navLine{text: l, entry: i})
		}
	}
	var foot []string
	for l := range strings.SplitSeq(ansi.Wordwrap(a.footer(), a.nw()-1, ""), "\n") {
		foot = append(foot, " "+l)
	}
	label := "─ intake "
	label += strings.Repeat("─", max(a.nw()-ansi.StringWidth(label), 0))
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
	// Scrolled just enough to show the selected entry, all its lines.
	first, last := -1, 0
	for i, l := range lines {
		if l.entry == a.sel {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	first = max(first, 0)
	var out []string
	for _, l := range lines {
		out = append(out, l.text)
	}
	a.navTop = min(max(a.navTop, last-room+1), first)
	a.navTop = max(min(a.navTop, len(out)-room), 0)
	out = out[a.navTop:min(a.navTop+room, len(out))]
	for len(out) < room {
		out = append(out, "")
	}
	return strings.Join(append(append(out, foot...), box...), "\n")
}

// navEntry renders one entry: a glyph for where it stands and its name cut
// to fit, and for a goal or the intake, the thing about it that matters most
// now on a line under it.
func (a *App) navEntry(i int, e entry) []string {
	var glyph, name, badge, under string
	switch e.kind {
	case entryNext:
		glyph, name = tui.Color("»", tui.Cyan), "Next"
		if n := len(a.next.items); n > 0 {
			badge = tui.Color(strconv.Itoa(n), tui.Magenta)
		}
	case entryIntake:
		glyph, name = tui.Color("+", tui.Blue), "Intake"
		if e.row != nil {
			under = relevant(*e.row)
		}
	case entryGoal:
		glyph, name, under = navGlyph(*e.row), e.row.goal.Title, relevant(*e.row)
		if name == "" {
			name = e.row.goal.Name
		}
	case entryFinished:
		glyph, name = tui.Dim("▸"), fmt.Sprintf("Finished (%d)", len(a.finished))
		if a.unfolded {
			glyph = tui.Dim("▾")
		}
	case entryFinishedGoal:
		glyph, name = tui.Dim("✓"), e.goal.Title
	}
	// Glyphs take two columns, as an emoji does.
	glyph += strings.Repeat(" ", max(2-ansi.StringWidth(glyph), 0))
	room := a.nw() - 5 - ansi.StringWidth(badge)
	if badge != "" {
		room--
	}
	name = ansi.Truncate(name, max(room, 4), "…")
	pad := strings.Repeat(" ", max(room-ansi.StringWidth(name), 0))
	if badge != "" {
		pad += " "
	}
	lines := []string{" " + glyph + " " + name + pad + badge}
	if under != "" {
		lines = append(lines, "    "+ansi.Truncate(under, max(a.nw()-5, 4), "…"))
	}
	if i != a.sel {
		return lines
	}
	for j, l := range lines {
		if a.focus == partNav {
			l = ansi.Strip(l)
			lines[j] = tui.SGR(
				7,
			) + l + strings.Repeat(
				" ",
				max(a.nw()-ansi.StringWidth(l), 0),
			) + tui.Reset
		} else {
			lines[j] = tui.Bold(l)
		}
	}
	return lines
}

// relevant is the one thing about a goal that matters most now, in its
// color: what waits on the human first, then what holds it up, then what it
// is doing.
func relevant(r goalRow) string {
	if r.intake {
		if r.counts[queue.Pending]+r.counts[queue.Active]+r.counts[queue.Blocked] == 0 {
			return ""
		}
		return intakeLine(r)
	}
	name, c := goalStatus(r)
	switch {
	case r.goal.State == queue.GoalPlanning && r.plan != nil && !r.sentBack:
		return tui.Color("plan to sign off", tui.Green)
	case r.questions > 0:
		return tui.Color(count(r.questions, "question"), tui.Magenta)
	case name == "ready to finish":
		return tui.Color("ready to merge into "+r.goal.Base, c)
	case r.toReview > 0:
		return tui.Color(count(r.toReview, "hunk")+" to review", tui.Yellow)
	case name == "blocked":
		waits := r.waiting[0]
		if n := len(r.waiting) - 1; n > 0 {
			waits += fmt.Sprintf(" and %d more", n)
		}
		return tui.Color("waits for "+waits, c)
	case len(r.activeWork) > 0:
		var ws []string
		for _, w := range r.activeWork {
			if w == "" {
				w = "grilling"
			}
			ws = append(ws, w)
		}
		return tui.Color("running "+strings.Join(ws, ", "), c)
	case name == "queued":
		return tui.Color(fmt.Sprintf("%d ready, waiting for a session", r.ready), c)
	case r.goal.State == queue.GoalPlanning:
		return planningLine(r)
	case r.goal.State == queue.GoalDone:
		return landingLine(r)
	case r.goal.State == queue.GoalParked:
		return tui.Color("parked", c)
	}
	if left := r.counts[queue.Pending] + r.counts[queue.Blocked]; left > 0 {
		return tui.Dim(count(left, "task") + " left")
	}
	return ""
}

// count is n of a thing, in the plural when it isn't one.
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// navGlyph is where a goal stands, as the nav shows it.
func navGlyph(r goalRow) string {
	name, c := goalStatus(r)
	g, ok := map[string]string{
		"active": "🟢", "queued": "⏳", "blocked": "🔗", "reviewing": "🔎", "ready to finish": "📩",
		"planning": "📝", "parked": "⏸️", "done": "✅",
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
	if u := a.update; u != nil {
		parts = append(parts, tui.Color(u.Why+" · U restarts on it", tui.Green))
	}
	if a.flash != "" {
		parts = append(parts, tui.Color(a.flash, tui.Cyan))
	}
	return strings.Join(parts, tui.Dim(" · "))
}

func (a *App) renderMain() string {
	if a.quitting {
		return "Suspending the running sessions; they carry on where they stopped the next time " +
			"diatom opens.\n\n" + tui.Dim("ctrl+c again stops at once, leaving any git work half done.")
	}
	if a.logOpen {
		return a.renderLog(a.mainWidth(), a.height)
	}
	if a.review != nil {
		head := a.reviewHead()
		a.review.SetSize(a.mainWidth(), max(a.height-len(head), 5))
		return strings.Join(append(head, a.review.Render()), "\n")
	}
	e := a.selected()
	switch e.kind {
	case entryNext:
		return a.next.render(a.focus == partMain, a.status.foot())
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

// onScreen is what the main pane shows, for an intake sent now: the goal it
// is about, and a clue for triage.
func (a *App) onScreen() (goal, context string) {
	e := a.selected()
	switch e.kind {
	case entryNext:
		return a.next.onScreen()
	case entryIntake:
		return "", "the intake, with what triage is still sorting"
	case entryFinishedGoal:
		return "", fmt.Sprintf("the finished goal %s (%q)", e.goal.Name, e.goal.Title)
	case entryGoal:
		g := e.row.goal
		context = fmt.Sprintf("goal %s (%q)", g.Name, g.Title)
		if d := a.status.detail; d != nil && d.task != nil && d.task.task != nil {
			context += fmt.Sprintf(", its task %s: %s", d.task.id, d.task.task.Title)
		}
		return g.Name, context
	}
	return "", ""
}

// notify tells the terminal, while it doesn't have the keyboard, that Next
// has something again after having nothing: once something waits, the
// human knows, and is seeing to other things first.
func (a *App) notify() tea.Cmd {
	was := a.waiting
	a.waiting = len(a.next.items)
	if was > 0 || a.waiting == 0 || !a.blurred {
		return nil
	}
	body := fmt.Sprintf("%d things wait on you", a.waiting)
	if a.waiting == 1 {
		body = "something waits on you"
	}
	if it := a.next.shown(); it != nil {
		body += ": " + it.row.goal.Name
	}
	return tea.Raw("\x1b]777;notify;diatom;" + body + "\x07")
}

// reviewHead says, above a goal's review opened from its page, what the goal
// is and what it is for.
func (a *App) reviewHead() []string {
	row := a.status.pageRow()
	if row == nil {
		return nil
	}
	w := max(a.mainWidth()-2, 20)
	title := row.goal.Title
	if title == "" {
		title = row.goal.Name
	}
	lines := []string{"‹ " + tui.Bold(title) + tui.Dim(" · "+row.goal.Name)}
	if d := row.description; d != "" {
		for _, l := range clipLines(d, w, 4) {
			lines = append(lines, tui.Dim(l))
		}
	}
	return append(lines, tui.Dim(strings.Repeat("─", w+2)))
}
