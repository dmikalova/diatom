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
	// sel runs over the goal's actions, then its tasks; top is the first
	// line on screen.
	sel, top int
	// task is the task opened; nil shows the goal.
	task *taskView
	// latest is the latest step of each running task's session, from the
	// last reload.
	latest map[string]string
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
	d.sel = min(d.sel, max(len(s.detailActions())+len(tasks)-1, 0))
	d.latest = map[string]string{}
	for _, t := range tasks {
		if t.State != queue.Active {
			continue
		}
		if sv, _ := latestSession(store.SessionsDir(d.goal), t.ID); sv != nil {
			d.latest[t.ID] = sv.latest()
		}
	}
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
	acts := s.detailActions()
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
		d.sel = min(d.sel+1, max(len(acts)+len(d.tasks)-1, 0))
	case "k", "up":
		d.sel = max(d.sel-1, 0)
	case "enter", "space", " ", "right", "l":
		switch {
		case d.sel < len(acts):
			if isEnter(key) {
				cmd, _ := s.act(s.detailRow(), acts[d.sel].key)
				return cmd
			}
		case d.sel-len(acts) < len(d.tasks):
			tv, err := loadTask(s.env.Store, d.goal, d.tasks[d.sel-len(acts)].ID)
			if err != nil {
				s.err = err
				break
			}
			d.task = tv
		}
	}
	return nil
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

// renderMenu shows where a goal stands, what can be done with it, and its
// tasks, the running ones first with their latest step, scrolled to keep the
// one selected in view.
func (s *Status) renderMenu(d *detail, room int) string {
	var lines []string
	if row := s.pageRow(); row != nil {
		lines = s.pageHead(*row)
		lines = append(lines, s.jobLines(row.goal.Name, room)...)
	}
	sel := 0
	acts := s.detailActions()
	for i, a := range acts {
		mark := "  "
		if i == d.sel {
			mark, sel = tui.Color("› ", tui.Accent), len(lines)
		}
		lines = append(lines, mark+tui.Color(a.key, tui.Yellow)+"  "+a.label)
	}
	if len(acts) > 0 {
		lines = append(lines, "")
	}
	row := s.detailRow()
	if row != nil && row.plan != nil {
		lines = append(lines, tui.Dim("─── plan"))
		lines = append(
			lines,
			strings.Split(strings.TrimRight(plan.Describe(row.plan), "\n"), "\n")...)
		lines = append(lines, "", tui.Dim("─── tasks"))
	}
	if len(d.tasks) == 0 {
		lines = append(lines, tui.Dim("No tasks."))
	}
	for i, t := range d.tasks {
		mark := "  "
		if len(acts)+i == d.sel {
			mark, sel = tui.Color("› ", tui.Accent), len(lines)
		}
		lines = append(lines, s.taskLine(d, t, mark))
	}
	return scroll(lines, sel, sel, &d.top, room, s.width)
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
	return append(lines, "")
}

// taskLine is one of a goal's tasks, with its latest step while it runs.
func (s *Status) taskLine(d *detail, t *queue.Task, mark string) string {
	ws := ""
	if t.Workstream != "" {
		ws = tui.Dim("[" + t.Workstream + "] ")
	}
	var line strings.Builder
	fmt.Fprintf(&line, "%s%s %s %s%s", mark, stateMark(t.State), t.ID, ws, t.Title)
	switch t.State {
	case queue.Active:
		if st := d.latest[t.ID]; st != "" {
			line.WriteString(tui.Dim(" · " + oneLine(st, s.width/2)))
		}
	case queue.Blocked:
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
