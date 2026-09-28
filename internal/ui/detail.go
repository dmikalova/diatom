package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// detail is a goal, or the intake, opened from the status list: what can be
// done with it and its tasks, and one task opened from those.
type detail struct {
	goal  string
	title string
	tasks []*queue.Task
	// sel runs over the goal's running sessions, its actions, then its
	// tasks; top is the first line on screen.
	sel, top int
	// task is the task opened; nil shows the goal.
	task *taskView
	// head is how many rows of the page come before its menu, and rows what
	// each of the menu's rows on screen picks for sel, -1 for nothing, both
	// from the last render, for a click to find what it is on.
	head int
	rows []int
}

// openDetail opens a row of the status list.
func (s *Status) openDetail(r *goalRow) {
	s.detail = &detail{goal: r.goal.Name, title: r.goal.Title}
	if s.detail.title == "" {
		s.detail.title = r.goal.Name
	}
	if r.intake {
		s.detail.title = "Intake"
	}
	s.reloadDetail()
}

// reloadDetail rereads what the open detail shows, so it follows the work
// live.
func (s *Status) reloadDetail() {
	d := s.detail
	if d == nil {
		return
	}
	store := s.env.Store
	tasks, err := store.Tasks(d.goal)
	if err != nil {
		s.loadErr = err
		return
	}
	order := map[queue.State]int{queue.Active: 0, queue.Blocked: 1, queue.Pending: 2, queue.Done: 3}
	slices.SortStableFunc(
		tasks,
		func(a, b *queue.Task) int { return order[a.State] - order[b.State] },
	)
	d.tasks = tasks
	d.sel = min(d.sel, max(s.detailItems()-1, 0))
	if d.task == nil {
		return
	}
	tv, err := loadTask(store, d.goal, d.task.id)
	if err != nil {
		s.loadErr = err
		return
	}
	tv.keep(d.task)
	d.task = tv
}

// pageRunning are the workstreams of the page open with a session running,
// "" for triage's or grilling's.
func (s *Status) pageRunning() []string {
	if row := s.pageRow(); row != nil {
		return row.activeWork
	}
	return nil
}

// detailItems counts what the page open can select: its running sessions,
// its actions and its tasks.
func (s *Status) detailItems() int {
	return len(s.pageRunning()) + len(s.detailActions()) + len(s.detail.tasks)
}

// detailRow is the status row of the goal open, nil for the intake or a
// goal gone from the list.
func (s *Status) detailRow() *goalRow {
	for i := range s.rows {
		if !s.rows[i].intake && s.rows[i].goal.Name == s.detail.goal {
			return &s.rows[i]
		}
	}
	return nil
}

// detailActions are what can be done with the goal open: its actions, and
// reviewing it where the window can.
// jobLines show, on the page of the goal a job works on, what it is doing
// and its log as it goes, or once it has ended, until the human moves on.
func (s *Status) jobLines(goal string, room int) []string {
	if i := s.queuedAt(goal); i >= 0 {
		return []string{"  " + tui.Color(fmt.Sprintf("⏳ %s once %s has landed", s.queued[i].what,
			s.busyGoal), tui.Yellow), ""}
	}
	if s.log == nil || s.logGoal != goal {
		return nil
	}
	var lines []string
	if s.busyGoal == goal {
		lines = append(lines, "  "+tui.Color("▶ "+s.busy+"…", tui.Green))
	}
	for _, l := range s.log.tail(max(room/2, 5)) {
		lines = append(lines, "    "+tui.Dim(oneLine(l, max(s.width-6, 20))))
	}
	return append(lines, "")
}

// pageRow is the row of the goal or intake open, nil when it is gone.
func (s *Status) pageRow() *goalRow {
	for i := range s.rows {
		if s.rows[i].goal.Name == s.detail.goal {
			return &s.rows[i]
		}
	}
	return nil
}

func (s *Status) detailActions() []action {
	row := s.detailRow()
	if row != nil && s.landing(row.goal.Name) {
		// Nothing else is offered while the goal is being landed, or waits
		// to be.
		return nil
	}
	acts := actions(row)
	if s.openReview != nil && row != nil && !row.intake && row.toReview > 0 {
		acts = append([]action{{"r", fmt.Sprintf("Review its %d hunks", row.toReview)}}, acts...)
	}
	if row != nil && row.questions > 0 {
		// a answers them, and goes on to the plan when there is one.
		acts = slices.DeleteFunc(acts, func(a action) bool { return a.key == "a" })
		label := "Answer its " + count(row.questions, "question")
		if row.manual == row.questions {
			label = "Do its " + manualLabel(row.manual)
		}
		acts = append([]action{{"a", label}}, acts...)
	}
	return acts
}

// updateDetail handles keys while a goal or anything in it is open: enter or
// space does the action or opens what is selected, a goal's keys work as in
// the list, and esc backs out one level.
func (s *Status) updateDetail(msg tea.KeyPressMsg) tea.Cmd {
	d := s.detail
	key := msg.String()
	if d.task != nil {
		if d.task.update(s, key) {
			d.task = nil
		}
		return nil
	}
	if row := s.detailRow(); row != nil {
		if cmd, ok := s.act(row, key); ok {
			return cmd
		}
	}
	if !isEnter(key) {
		s.confirm = ""
	}
	switch key {
	case "esc", "left":
		s.detail = nil
	case "j", "down":
		d.sel = min(d.sel+1, max(s.detailItems()-1, 0))
	case "k", "up":
		d.sel = max(d.sel-1, 0)
	case "enter", "space", " ", "right", "l":
		return s.choose(isEnter(key))
	}
	return nil
}

// choose does what the page has selected: opens a running session on its
// latest step, or a task, or, on enter, does an action.
func (s *Status) choose(enter bool) tea.Cmd {
	d := s.detail
	running, acts := s.pageRunning(), s.detailActions()
	i := d.sel
	switch {
	case i < len(running):
		row := s.pageRow()
		tv, err := openSession(s.env.Store, d.goal, row.sessions[running[i]])
		if err != nil {
			s.err = err
			return nil
		}
		d.task = tv
	case i-len(running) < len(acts):
		if enter {
			cmd, _ := s.act(s.detailRow(), acts[i-len(running)].key)
			return cmd
		}
	case i-len(running)-len(acts) < len(d.tasks):
		tv, err := loadTask(s.env.Store, d.goal, d.tasks[i-len(running)-len(acts)].ID)
		if err != nil {
			s.err = err
			return nil
		}
		d.task = tv
	}
	return nil
}

// clickDetail does what a click on row y of the page is on, as enter would.
func (s *Status) clickDetail(y int) tea.Cmd {
	d := s.detail
	if d == nil || d.task != nil {
		return nil
	}
	r := y - d.head
	if r < 0 || r >= len(d.rows) || d.rows[r] < 0 {
		return nil
	}
	if d.sel != d.rows[r] {
		// A second click confirms what asks for one, as enter does.
		s.confirm = ""
	}
	d.sel = d.rows[r]
	return s.choose(true)
}

// isEnter reports whether key chooses what a menu has selected: enter, or
// space.
func isEnter(key string) bool { return key == "enter" || key == "space" || key == " " }

func (s *Status) renderDetail() string {
	d := s.detail
	var b strings.Builder
	b.WriteString(tui.Dim("‹ ") + tui.Bold(d.title))
	if d.task != nil {
		b.WriteString(d.task.crumbs())
	}
	d.head = strings.Count(hangAll(b.String(), max(s.width, 1)), "\n") + 2
	b.WriteString("\n\n")
	foot := s.foot()
	room := max(s.height-2-len(foot), 3)
	if d.task != nil {
		b.WriteString(d.task.render(s, room))
	} else {
		b.WriteString(s.renderMenu(d, room))
	}
	if len(foot) > 0 {
		b.WriteString("\n\n" + strings.Join(foot, "\n"))
	}
	return b.String()
}

// renderMenu shows where a goal stands, its running sessions with their
// latest step, what can be done with it, and its tasks, scrolled to keep the
// one selected in view.
func (s *Status) renderMenu(d *detail, room int) string {
	var lines []string
	var pick []int
	add := func(item int, l ...string) {
		for _, x := range l {
			lines, pick = append(lines, x), append(pick, item)
		}
	}
	n, sel := 0, 0
	// item adds a line that can be selected, marked while it is.
	item := func(text string) {
		mark := "  "
		if n == d.sel {
			mark, sel = tui.Color("› ", tui.Accent), len(lines)
		}
		add(n, mark+text)
		n++
	}
	row := s.pageRow()
	if row != nil {
		add(-1, s.pageHead(*row)...)
		for _, ws := range row.activeWork {
			name := ws
			if ws == "" {
				name = "planning"
			}
			item(tui.Color("▶ "+name, tui.Green) + " " + tui.Dim(row.latest[ws]))
		}
		add(-1, "")
		add(-1, s.jobLines(row.goal.Name, room)...)
	}
	acts := s.detailActions()
	for _, a := range acts {
		item(tui.Color(a.key, tui.Yellow) + "  " + a.label)
	}
	if len(acts) > 0 {
		add(-1, "")
	}
	if goal := s.detailRow(); goal != nil && goal.plan != nil {
		add(-1, tui.Dim("─── plan"))
		add(-1, strings.Split(strings.TrimRight(plan.Describe(goal.plan), "\n"), "\n")...)
		add(-1, "", tui.Dim("─── tasks"))
	}
	if len(d.tasks) == 0 {
		add(-1, tui.Dim("No tasks."))
	}
	for _, t := range d.tasks {
		item(s.taskLine(d, t))
	}
	out, rows := scrollRows(lines, pick, sel, sel, &d.top, room, s.width)
	d.rows = rows
	return out
}

// pageHead says where the goal or intake open stands: its state, what it is
// for, its tasks, and what it waits for.
func (s *Status) pageHead(row goalRow) []string {
	var rb strings.Builder
	s.renderRow(&rb, row)
	// The row's first line repeats the title; the rest says where it stands.
	lines := strings.Split(strings.TrimRight(rb.String(), "\n"), "\n")[1:]
	for i := range lines {
		lines[i] = strings.TrimPrefix(lines[i], "    ")
	}
	head := []string{"  " + intakeLine(row)}
	if !row.intake {
		head = []string{"  " + goalState(row) + tui.Dim(" · "+row.goal.Name)}
		if d := row.description; d != "" {
			for _, l := range hang(d, max(s.width-2, 20)) {
				head = append(head, "  "+l)
			}
		}
	}
	lines = append(head, lines...)
	if len(row.waiting) > 0 {
		lines = append(
			lines,
			"  "+tui.Color("blocked: waits for "+strings.Join(row.waiting, ", ")+
				" to finish, and nothing of it starts until then", tui.Red),
		)
	}
	return lines
}

// taskLine is one of a goal's tasks: the page's running sessions say what
// the running ones are doing.
func (s *Status) taskLine(d *detail, t *queue.Task) string {
	ws := ""
	if t.Workstream != "" {
		ws = tui.Dim("[" + t.Workstream + "] ")
	}
	var line strings.Builder
	fmt.Fprintf(&line, "%s %s %s%s", stateMark(t.State), t.ID, ws, t.Title)
	if t.State == queue.Blocked {
		line.WriteString(tui.Color(" · waiting on your answer", tui.Magenta))
	}
	for _, r := range s.rows {
		if r.goal.Name == d.goal && r.taskCost[t.ID] > 0 {
			line.WriteString(tui.Dim(fmt.Sprintf(" · $%.2f", r.taskCost[t.ID])))
		}
	}
	return line.String()
}

func stateMark(st queue.State) string {
	switch st {
	case queue.Active:
		return tui.Color("▶", tui.Green)
	case queue.Blocked:
		return tui.Color("?", tui.Magenta)
	case queue.Done:
		return tui.Dim("✓")
	}
	return tui.Dim("○")
}

// oneLine is the first line of s, cut to width.
func oneLine(s string, width int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	width = max(width, 20)
	if r := []rune(line); len(r) > width {
		line = string(r[:width]) + "…"
	}
	return line
}
