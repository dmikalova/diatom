package panes

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
	"github.com/dmikalova/diatom/internal/tui"
)

// detail is a goal, or the intake, opened from the status list: its tasks,
// and one task opened from those with what its agent is doing.
type detail struct {
	goal  string
	title string
	tasks []*queue.Task
	sel   int
	// task is the task opened, with its latest session; nil shows the list.
	task *taskView
}

// taskView is one task and the session that last worked on it.
type taskView struct {
	id        string
	task      *queue.Task
	questions []*queue.Question
	session   *sessionView
}

// sessionView is what a session's directory says about it.
type sessionView struct {
	id     string
	events []event
	// ended is set once the agent's part is over, with how it went.
	ended   bool
	outcome string
	turns   int
	costUSD float64
	err     string
	settled bool
}

// event is one step of a session, as runSession logs it.
type event struct {
	Time time.Time `json:"time"`
	Type string    `json:"type"`
	Text string    `json:"text"`
}

// openDetail opens a row of the status list.
func (s *Status) openDetail(r *goalRow) {
	s.detail = &detail{goal: r.goal.Name, title: r.goal.Title}
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
		s.err = err
		return
	}
	order := map[queue.State]int{queue.Active: 0, queue.Blocked: 1, queue.Pending: 2, queue.Done: 3}
	slices.SortStableFunc(
		tasks,
		func(a, b *queue.Task) int { return order[a.State] - order[b.State] },
	)
	d.tasks = tasks
	d.sel = min(d.sel, max(len(tasks)-1, 0))
	if d.task == nil {
		return
	}
	tv, err := loadTask(store, d.goal, d.task.id)
	if err != nil {
		s.err = err
		return
	}
	d.task = tv
}

// loadTask reads a task, its questions and the last session that worked on
// it.
func loadTask(s *queue.Store, goal, id string) (*taskView, error) {
	t, err := s.Task(goal, id)
	if err != nil {
		return nil, err
	}
	tv := &taskView{id: id, task: t}
	for _, st := range []queue.QuestionState{queue.QuestionOpen, queue.QuestionClosed} {
		qs, err := s.Questions(goal, st)
		if err != nil {
			return nil, err
		}
		for _, q := range qs {
			if q.Task == id {
				tv.questions = append(tv.questions, q)
			}
		}
	}
	tv.session, err = lastSession(s.SessionsDir(goal), id)
	return tv, err
}

// lastSession finds the newest session that worked on task, or nil.
func lastSession(root, task string) (*sessionView, error) {
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Newest first: the names start with the time.
	for _, d := range slices.Backward(dirs) {
		dir := filepath.Join(root, d.Name())
		spec, err := session.Load(dir)
		if err != nil || !slices.Contains(spec.Tasks, task) {
			continue
		}
		sv := &sessionView{id: spec.ID}
		if sv.events, err = readEvents(filepath.Join(dir, "events.jsonl")); err != nil {
			return nil, err
		}
		var res struct {
			Outcome string `json:"outcome"`
			Turns   int    `json:"turns"`
			Usage   struct {
				CostUSD float64 `json:"costUSD"`
			} `json:"usage"`
			Error string `json:"error"`
		}
		if sv.ended, err = session.ReadResult(dir, &res); err != nil {
			return nil, err
		}
		sv.outcome, sv.turns, sv.costUSD, sv.err = res.Outcome, res.Turns, res.Usage.CostUSD, res.Error
		st, err := session.LoadState(dir)
		if err != nil {
			return nil, err
		}
		sv.settled = st.Settled
		return sv, nil
	}
	return nil, nil
}

func readEvents(path string) ([]event, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var events []event
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	return events, sc.Err()
}

// updateDetail handles keys while a goal or task is open: esc backs out one
// level.
func (s *Status) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := s.detail
	switch msg.String() {
	case "q", "ctrl+c":
		return s, tea.Quit
	case "esc", "left", "h":
		if d.task != nil {
			d.task = nil
		} else {
			s.detail = nil
		}
	case "j", "down":
		d.sel = min(d.sel+1, max(len(d.tasks)-1, 0))
	case "k", "up":
		d.sel = max(d.sel-1, 0)
	case "enter", "right", "l":
		if d.task == nil && d.sel < len(d.tasks) {
			tv, err := loadTask(s.env.Store, d.goal, d.tasks[d.sel].ID)
			if err != nil {
				s.err = err
				break
			}
			d.task = tv
		}
	}
	return s, nil
}

func (s *Status) renderDetail() string {
	d := s.detail
	var b strings.Builder
	b.WriteString(tui.Dim("‹ ") + tui.Bold(d.title))
	if d.task != nil {
		b.WriteString(tui.Dim(" › ") + tui.Bold(d.task.id) + " " + d.task.task.Title)
	}
	b.WriteString("\n\n")
	if d.task != nil {
		s.renderTask(&b, d.task)
	} else {
		s.renderTasks(&b, d)
	}
	if s.err != nil {
		b.WriteString("\n" + tui.Color(s.err.Error(), tui.Red) + "\n")
	}
	help := "enter open · j/k move · esc back · q quit"
	if d.task != nil {
		help = "esc back · q quit"
	}
	b.WriteString("\n" + tui.Dim(help))
	return b.String()
}

// renderTasks lists a goal's tasks, the running ones first with their latest
// step.
func (s *Status) renderTasks(b *strings.Builder, d *detail) {
	if len(d.tasks) == 0 {
		b.WriteString(tui.Dim("No tasks.") + "\n")
	}
	for i, t := range d.tasks {
		mark := "  "
		if i == d.sel {
			mark = tui.Color("› ", tui.Cyan)
		}
		ws := ""
		if t.Workstream != "" {
			ws = tui.Dim("[" + t.Workstream + "] ")
		}
		fmt.Fprintf(b, "%s%s %s %s%s", mark, stateMark(t.State), t.ID, ws, t.Title)
		switch t.State {
		case queue.Active:
			if sv, _ := lastSession(
				s.env.Store.SessionsDir(d.goal),
				t.ID,
			); sv != nil &&
				len(sv.events) > 0 {
				b.WriteString(tui.Dim(" · " + oneLine(sv.events[len(sv.events)-1].Text, s.width/2)))
			}
		case queue.Blocked:
			b.WriteString(tui.Color(" · waiting on your answer", tui.Magenta))
		}
		b.WriteString("\n")
	}
}

// renderTask shows a task's session, as much of its steps as fit, then its
// questions and text.
func (s *Status) renderTask(b *strings.Builder, tv *taskView) {
	t := tv.task
	fmt.Fprintf(b, "%s %s · %s", stateMark(t.State), t.Kind, t.State)
	if t.Profile != "" {
		b.WriteString(tui.Dim(" · " + t.Profile))
	}
	b.WriteString("\n")

	var tail []string
	for _, q := range tv.questions {
		state := tui.Color("open", tui.Magenta)
		if q.Answer != "" {
			state = tui.Dim("answered")
		}
		tail = append(tail, fmt.Sprintf("? %s %s", oneLine(q.Text, s.width-14), state))
	}
	if body := strings.TrimSpace(t.Body); body != "" {
		tail = append(tail, "", tui.Dim("─── task"))
		lines := strings.Split(body, "\n")
		if len(lines) > 8 {
			lines = append(lines[:8], "…")
		}
		tail = append(tail, lines...)
	}

	sv := tv.session
	if sv == nil {
		b.WriteString(tui.Dim("No session has worked on it yet.") + "\n")
	} else {
		b.WriteString(sessionLine(sv) + "\n")
		// The steps get what the rest leaves of the pane, the latest last.
		room := max(s.height-8-len(tail), 5)
		events := sv.events[max(len(sv.events)-room, 0):]
		if len(events) < len(sv.events) {
			b.WriteString(
				tui.Dim(fmt.Sprintf("  … %d earlier steps", len(sv.events)-len(events))) + "\n",
			)
		}
		for _, e := range events {
			text := oneLine(e.Text, s.width-14)
			if e.Type == "tool" {
				text = tui.Color(text, tui.Cyan)
			}
			fmt.Fprintf(b, "  %s %s\n", tui.Dim(e.Time.Local().Format("15:04:05")), text)
		}
	}
	if len(tail) > 0 {
		b.WriteString("\n" + strings.Join(tail, "\n") + "\n")
	}
}

// sessionLine says whether a session is running or how it ended.
func sessionLine(sv *sessionView) string {
	steps := fmt.Sprintf("%d steps", len(sv.events))
	switch {
	case !sv.ended && !sv.settled:
		since := ""
		if len(sv.events) > 0 {
			since = " for " + time.Since(sv.events[0].Time).Round(time.Second).String()
		}
		return tui.Color("▶ running"+since, tui.Green) + tui.Dim(" · "+steps+" · session "+sv.id)
	case sv.err != "":
		return tui.Color("✗ "+oneLine(sv.err, 80), tui.Red) + tui.Dim(" · "+steps)
	}
	return tui.Dim(fmt.Sprintf("ended %s · %d turns · $%.2f · %s · session %s",
		sv.outcome, sv.turns, sv.costUSD, steps, sv.id))
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
