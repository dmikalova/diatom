package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/reviewui"
	"github.com/dmikalova/diatom/internal/termimg"
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
	// entrySpending breaks down what sessions cost by day, entryFinished
	// lists the finished goals, and entryLog is the scheduler's log, all in
	// the menu at the nav's foot.
	entrySpending
	entryFinished
	entryLog
)

// entry is one line of the nav.
type entry struct {
	kind entryKind
	// row is the goal's, or the intake's; nil for the intake before anything
	// is sent.
	row *goalRow
}

func (e entry) key() string {
	if e.row != nil {
		return e.row.goal.Name
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
	// finished are the goals landed upstream, the latest first, and
	// finishedTop how far down their list is scrolled.
	finished    []finishedGoal
	finishedTop int
	// spendSel is the day the spending selects, spendOpen set while it is
	// opened to its goals, and spendTop how far that is scrolled.
	spendSel, spendTop int
	spendOpen          bool
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
	// logBack is how many lines the log is scrolled back.
	logBack int
	// rowEntry is what each row of the nav selects, as last drawn: an entry,
	// or rowNone, rowFooter or rowIntake; labelRow is the intake box's rule.
	rowEntry []int
	labelRow int
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
	if a.status.busy == "" {
		a.status.log = nil
	}
	a.next.flash, a.next.err = "", nil
}

// Restart is the diatom to run once the app has quit, "" for none.
func (a *App) Restart() string { return a.restart }

func (a *App) reload() {
	a.status.reload()
	a.next.reload()
	a.intake.reload()
	a.finished = a.loadFinished()
	a.sel = min(a.sel, len(a.entries())-1)
}

// entries are the nav's lines: Next, the intake, the goals being worked on,
// and the menu: the finished goals and the scheduler's log.
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
	es = append(es, entry{kind: entrySpending})
	if len(a.finished) > 0 {
		es = append(es, entry{kind: entryFinished})
	}
	return append(es, entry{kind: entryLog})
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
	a.status.detail, a.review, a.logBack, a.finishedTop = nil, nil, 0, 0
	a.spendSel, a.spendTop, a.spendOpen = 0, 0, false
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
	m, cmd := a.handle(msg)
	if seq := a.images(); seq != "" {
		cmd = tea.Batch(cmd, tea.Raw(seq))
	}
	return m, cmd
}

// images is what the reviewer on screen has to send the terminal: the images
// of an image file's hunk. The frame is drawn first, as the reviewer sizes
// the images to it, and View then shows that frame.
func (a *App) images() string {
	rv := a.review
	if rv == nil && a.selected().kind == entryNext {
		rv = a.next.shownReviewer()
	}
	if rv == nil || a.quitting {
		return ""
	}
	a.frame, a.dirty = a.render(), false
	return rv.Images()
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
		cmd := a.status.jobDone(msg)
		a.next.reload()
		return a, cmd
	case jobTickMsg:
		if a.status.busy != "" {
			return a, jobTick()
		}
	case QuitMsg:
		return a.quit(false)
	case stoppedMsg:
		return a, exit()
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
			a.openLog()
			return a, a.setFocus(partMain)
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
		// Past either end, the nav comes round to the other.
		a.sel = (a.sel + 1) % len(a.entries())
		a.fromNext = false
	case "k", "up":
		n := len(a.entries())
		a.sel = (a.sel + n - 1) % n
		a.fromNext = false
	case "enter", "space", " ", "right", "l":
		if a.selected().kind == entryNext {
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
	if a.selected().kind == entryNext {
		return a.next.areas()
	}
	return 1
}

func (a *App) mainArea() int {
	if a.selected().kind == entryNext {
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
	switch a.selected().kind {
	case entryLog:
		if a.logKey(key) {
			return a, a.setFocus(partNav)
		}
		return a, nil
	case entryFinished:
		if a.finishedKey(key) {
			return a, a.setFocus(partNav)
		}
		return a, nil
	case entrySpending:
		if a.spendingKey(key) {
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
		return a, exit()
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

// exit quits, freeing first any images the window sent the terminal.
func exit() tea.Cmd {
	if !termimg.Sent() {
		return tea.Quit
	}
	return tea.Sequence(tea.Raw(termimg.DeleteAll), tea.Quit)
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
		if a.selected().kind == entryNext {
			return a, a.focusMain(int(a.next.areaAt(m.Y)))
		}
		return a, a.setFocus(partMain)
	}
	a.renderNav()
	row := rowNone
	if m.Y >= 0 && m.Y < len(a.rowEntry) {
		row = a.rowEntry[m.Y]
	}
	switch {
	case row == rowIntake:
		return a, a.setFocus(partIntake)
	case row == rowFooter:
		a.openLog()
		return a, a.setFocus(partMain)
	case row == rowSpend:
		a.openEntry(entrySpending)
		return a, a.setFocus(partMain)
	case row >= 0:
		a.sel = row
		a.show()
	}
	return a, a.setFocus(partNav)
}

// openLog selects the scheduler's log.
func (a *App) openLog() { a.openEntry(entryLog) }

// openEntry selects the menu's entry of kind.
func (a *App) openEntry(kind entryKind) {
	for i, e := range a.entries() {
		if e.kind == kind {
			a.sel = i
		}
	}
	a.show()
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
		// A notch of the wheel sends several events, and the nav moves one
		// entry for it, not one for each.
		a.sel = max(min(a.sel+max(min(steps, 1), -1), len(a.entries())-1), 0)
		a.fromNext = false
		a.show()
		return
	}
	if it := a.next.shown(); a.selected().kind == entryNext && it != nil &&
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
	v.WindowTitle = fmt.Sprintf("diatom · %s · %s today", filepath.Base(a.env.Store.Repo()),
		money(a.status.totals.Day))
	return v
}

func (a *App) render() string {
	mw := a.mainWidth()
	main := lipgloss.NewStyle().Width(mw).MaxWidth(mw).Height(a.height).MaxHeight(a.height).
		Render(hangAll(a.renderMain(), mw))
	nw := a.nw()
	if nw == 0 {
		return main
	}
	nav := lipgloss.NewStyle().Width(nw).MaxWidth(nw).Height(a.height).
		MaxHeight(a.height).Render(a.renderNav())
	// The border lights up beside what has the keyboard, and the intake box's
	// rule joins it.
	border := make([]string, a.height)
	for y := range border {
		ch := "│"
		if y == a.labelRow {
			ch = "┤"
		}
		on := a.focus == partMain || a.dragging || a.focus == partIntake && y >= a.labelRow
		if on {
			border[y] = tui.Color(ch, tui.Accent)
		} else {
			border[y] = tui.Dim(ch)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, nav, strings.Join(border, "\n"), main)
}

// navLine is one line of the nav, and the entry it selects, or -1.
type navLine struct {
	text  string
	entry int
}

// What a row of the nav selects, besides an entry.
const (
	rowNone   = -1
	rowFooter = -2
	rowIntake = -3
	rowSpend  = -4
)

// navLayout is the nav's parts: the list that scrolls, the menu at its foot,
// and under that the footer and the intake box's rule, each line with what
// clicking it opens.
type navLayout struct {
	list, menu, foot []navLine
}

// navLines lays the nav out.
func (a *App) navLines() navLayout {
	lay := navLayout{menu: []navLine{{text: tui.Dim(strings.Repeat("─", a.nw())), entry: rowNone}}}
	es := a.entries()
	for i, e := range es {
		if e.kind == entrySpending || e.kind == entryFinished || e.kind == entryLog {
			for _, l := range a.navEntry(i, e) {
				lay.menu = append(lay.menu, navLine{text: l, entry: i})
			}
			continue
		}
		if e.kind == entryGoal && (i == 0 || es[i-1].kind != entryGoal) {
			lay.list = append(lay.list, navLine{text: "", entry: rowNone})
		}
		for _, l := range a.navEntry(i, e) {
			lay.list = append(lay.list, navLine{text: l, entry: i})
		}
	}
	for _, part := range []struct {
		text string
		row  int
	}{{strings.Join(a.spending(), "\n"), rowSpend}, {strings.Join(a.health(), "\n"), rowFooter}} {
		if part.text == "" {
			continue
		}
		for l := range strings.SplitSeq(ansi.Wordwrap(part.text, a.nw()-1, ""), "\n") {
			lay.foot = append(lay.foot, navLine{text: " " + l, entry: part.row})
		}
	}
	label := "─ intake "
	label += strings.Repeat("─", max(a.nw()-ansi.StringWidth(label), 0))
	if a.focus == partIntake {
		label = tui.Color(label, tui.Accent)
	} else {
		label = tui.Dim(label)
	}
	lay.foot = append(lay.foot, navLine{text: label, entry: rowIntake})
	return lay
}

func (a *App) renderNav() string {
	lay := a.navLines()
	box := strings.Split(strings.TrimRight(a.intake.render(), "\n"), "\n")
	room := max(a.height-len(lay.menu)-len(lay.foot)-len(box), 1)
	// Scrolled just enough to show the selected entry, all its lines.
	first, last := -1, 0
	for i, l := range lay.list {
		if l.entry == a.sel {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first >= 0 {
		a.navTop = min(max(a.navTop, last-room+1), first)
	}
	a.navTop = max(min(a.navTop, len(lay.list)-room), 0)
	shown := lay.list[a.navTop:min(a.navTop+room, len(lay.list))]
	var out []string
	a.rowEntry = a.rowEntry[:0]
	for _, l := range shown {
		out, a.rowEntry = append(out, l.text), append(a.rowEntry, l.entry)
	}
	for len(out) < room {
		out, a.rowEntry = append(out, ""), append(a.rowEntry, rowNone)
	}
	for _, l := range lay.menu {
		out, a.rowEntry = append(out, l.text), append(a.rowEntry, l.entry)
	}
	for i, l := range lay.foot {
		if i == len(lay.foot)-1 {
			a.labelRow = len(out)
		}
		out, a.rowEntry = append(out, l.text), append(a.rowEntry, l.entry)
	}
	for _, l := range box {
		out, a.rowEntry = append(out, l), append(a.rowEntry, rowIntake)
	}
	return strings.Join(out, "\n")
}

// navEntry renders one entry: a glyph for where it stands and its name cut
// to fit, and for a goal or the intake, the thing about it that matters most
// now on a line under it.
func (a *App) navEntry(i int, e entry) []string {
	var glyph, name, under string
	switch e.kind {
	case entryNext:
		glyph, name, under = emoji("⏩"), "Next", a.nextCounts()
	case entryIntake:
		glyph, name = emoji("➕"), "Intake"
		if e.row != nil {
			under = relevant(*e.row)
		}
	case entryGoal:
		glyph, name, under = navGlyph(*e.row, a.status.busyGoal == e.row.goal.Name),
			e.row.goal.Title, relevant(*e.row)
		if name == "" {
			name = e.row.goal.Name
		}
		if a.status.busyGoal == e.row.goal.Name {
			under = tui.Color("▶ "+a.status.busy+"…", tui.Green)
		} else if a.status.queuedAt(e.row.goal.Name) >= 0 {
			glyph, under = emoji("⏳"), tui.Color("waiting to land", tui.Yellow)
		}
	case entrySpending:
		glyph, name = emoji("💰"), "Spending"
	case entryFinished:
		glyph, name = emoji("☑️"), fmt.Sprintf("Finished (%d)", len(a.finished))
	case entryLog:
		glyph, name = emoji("📒"), "Scheduler log"
	}
	// Glyphs take two columns, as an emoji does.
	glyph += strings.Repeat(" ", max(2-ansi.StringWidth(glyph), 0))
	title := " " + ansi.Truncate(name, max(a.nw()-5, 4), "…")
	lead := " "
	if i == a.sel {
		// The selected entry has a bar down its left, and its title is bold:
		// it stays lit while the main pane shows it. The glyph isn't bold,
		// which would draw an emoji from the text font.
		lead, title = tui.Color("▌", tui.Accent), boldAll(title)
	}
	lines := []string{lead + glyph + title}
	if under != "" {
		lines = append(lines, lead+"   "+ansi.Truncate(under, max(a.nw()-5, 4), "…"))
	}
	return lines
}

// emoji asks for s in its color emoji form, as a terminal otherwise may draw
// it from the text font.
func emoji(s string) string {
	if strings.HasSuffix(s, "\uFE0F") {
		return s
	}
	return s + "\uFE0F"
}

// boldAll renders s bold throughout, the colors in it included: each of its
// resets would otherwise end the bold.
func boldAll(s string) string {
	return tui.SGR(1) + strings.ReplaceAll(s, tui.Reset, tui.Reset+tui.SGR(1)) + tui.Reset
}

// nextCounts sums up what waits across every goal: plans to sign off,
// questions, goals ready to finish, hunks to review, and goals blocked.
func (a *App) nextCounts() string {
	var plans, questions, finishing, hunks, blocked int
	for _, it := range a.next.items {
		switch it.kind {
		case itemPlan:
			plans++
		case itemQuestion:
			questions++
		case itemFinish:
			finishing++
		case itemReview:
			hunks += it.row.toReview
		}
	}
	for _, r := range a.status.rows {
		if name, _ := goalStatus(r); name == "blocked" {
			blocked++
		}
	}
	var parts []string
	for _, c := range []struct {
		emoji string
		n     int
	}{{"📝", plans}, {"❓", questions}, {"📩", finishing}, {"🔎", hunks}, {"🔗", blocked}} {
		if c.n > 0 {
			parts = append(parts, emoji(c.emoji)+" "+strconv.Itoa(c.n))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, tui.Dim("nothing needs you"))
	}
	// And what runs, which needs nothing of the human: agents, and git
	// work, a landing's included.
	agents, merging := a.running()
	if agents > 0 {
		parts = append(parts, emoji("🤖")+" "+strconv.Itoa(agents))
	}
	if merging > 0 {
		parts = append(parts, emoji("🔀")+" "+strconv.Itoa(merging))
	}
	return strings.Join(parts, "  ")
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
		// The goal's page names what it waits for.
		return tui.Color("blocked", c)
	case len(r.activeWork) > 0:
		var running, merging []string
		for _, w := range r.activeWork {
			settles := slices.Contains(r.settling, w)
			if w == "" {
				w = "grilling"
			}
			if settles {
				merging = append(merging, w)
			} else {
				running = append(running, w)
			}
		}
		var parts []string
		if len(running) > 0 {
			parts = append(parts, "running "+strings.Join(running, ", "))
		}
		if len(merging) > 0 {
			parts = append(parts, "committing "+strings.Join(merging, ", "))
		}
		return tui.Color(strings.Join(parts, "; "), c)
	case name == "queued":
		return tui.Color("queued", c)
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

// navGlyph is where a goal stands, as the nav shows it: a robot while an
// agent works on it, and the merge sign while diatom does its git work, such
// as committing a session's work or landing it.
func navGlyph(r goalRow, landing bool) string {
	name, c := goalStatus(r)
	switch {
	case landing:
		return emoji("🔀")
	case len(r.activeWork) > len(r.settling):
		return emoji("🤖")
	case len(r.settling) > 0:
		return emoji("🔀")
	}
	g, ok := map[string]string{
		"active": "🟢", "queued": "⏳", "blocked": "🔗", "reviewing": "🔎", "ready to finish": "📩",
		"planning": "📝", "parked": "⏸️", "done": "✅",
	}[name]
	if !ok {
		return tui.Color("·", c)
	}
	return emoji(g)
}

// running counts the agents running, and the git work under way: the
// sessions diatom is committing, and a landing.
func (a *App) running() (agents, merging int) {
	for _, r := range a.status.rows {
		agents += len(r.activeWork) - len(r.settling)
		merging += len(r.settling)
	}
	if a.status.busy != "" {
		merging++
	}
	return agents, merging
}

// money is an amount in dollars, to the cent while it is small.
func money(usd float64) string {
	if usd >= 10 {
		return fmt.Sprintf("$%.0f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// spending is what the repo's sessions cost today, this week and this month,
// in red where it has spent the budget, and what that holds up.
func (a *App) spending() []string {
	var scales []string
	for _, sc := range a.status.totals.Scales(a.status.budget) {
		t := money(sc.Spent) + sc.Letter
		if sc.Over() {
			t = tui.Color(t, tui.Red)
		}
		scales = append(scales, t)
	}
	out := []string{strings.Join(scales, tui.Dim(" · "))}
	if over := a.status.totals.Over(a.status.budget); over != "" {
		out = append(out, tui.Color(over+" budget is spent: nothing new starts", tui.Red))
	}
	return out
}

// footer is the repo's health, a line for each thing: what it cost, and the
// scheduler.
func (a *App) footer() string {
	return strings.Join(append(a.spending(), a.health()...), "\n")
}

// health is how the scheduler is, and what else the footer says.
func (a *App) health() []string {
	var parts []string
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
	return parts
}

func (a *App) renderMain() string {
	if a.quitting {
		return "Suspending the running sessions; they carry on where they stopped the next time " +
			"diatom opens.\n\n" + tui.Dim("ctrl+c again stops at once, leaving any git work half done.")
	}
	if a.selected().kind == entryLog {
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
		return a.renderFinished(a.mainWidth(), a.height)
	case entrySpending:
		return a.renderSpending(a.mainWidth(), a.height)
	}
	return ""
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
	case entryFinished:
		return "", "the list of finished goals"
	case entrySpending:
		return "", "what the sessions spent, day by day"
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
