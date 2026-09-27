package panes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
	"github.com/dmikalova/diatom/internal/tui"
)

// taskView is a task opened from a goal: the Claude sessions that worked on
// it, newest first, and one session opened from those.
type taskView struct {
	id        string
	task      *queue.Task
	deps      []*queue.Task
	questions []*queue.Question
	sessions  []*sessionView
	sel, top  int
	open      *sessionView
}

// sessionView is one session of a task: how it went, and its steps.
type sessionView struct {
	id      string
	started time.Time
	steps   []step
	// ended is set once the agent's part is over, with how it went.
	ended   bool
	outcome string
	turns   int
	costUSD float64
	err     string
	settled bool
	// settleErr is why diatom couldn't settle the session's work.
	settleErr string
	// sel is the step selected, and follow keeps it on the latest while the
	// session runs; open is the step opened, -1 for none, and scroll how far
	// down it is.
	sel, top     int
	follow       bool
	open, scroll int
}

// step is one thing a session did: something the agent said, a tool call
// with its result, or a gate run.
type step struct {
	at      time.Time
	kind    string
	summary string
	input   string
	output  string
	failed  bool
	// done is set once a tool call has its result, after took.
	done bool
	took time.Duration
}

// loadTask reads a task, its questions and the sessions that worked on it.
func loadTask(s *queue.Store, goal, id string) (*taskView, error) {
	t, err := s.Task(goal, id)
	if err != nil {
		return nil, err
	}
	tv := &taskView{id: id, task: t}
	for _, d := range t.DependsOn {
		// A plan can name a dependency twice.
		if dt, err := s.Task(goal, d); err == nil &&
			!slices.ContainsFunc(tv.deps, func(x *queue.Task) bool { return x.ID == dt.ID }) {
			tv.deps = append(tv.deps, dt)
		}
	}
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
	tv.sessions, err = taskSessions(s.SessionsDir(goal), id, 0)
	return tv, err
}

// latestSession is the newest session that worked on task, or nil.
func latestSession(root, task string) (*sessionView, error) {
	svs, err := taskSessions(root, task, 1)
	if len(svs) == 0 {
		return nil, err
	}
	return svs[0], err
}

// taskSessions reads the sessions that worked on task, newest first, up to
// limit of them when it isn't 0.
func taskSessions(root, task string, limit int) ([]*sessionView, error) {
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var svs []*sessionView
	// Newest first: the names start with the time.
	for _, d := range slices.Backward(dirs) {
		dir := filepath.Join(root, d.Name())
		spec, err := session.Load(dir)
		if err != nil || !slices.Contains(spec.Tasks, task) {
			continue
		}
		sv, err := loadSession(dir, spec.ID)
		if err != nil {
			return nil, err
		}
		if svs = append(svs, sv); len(svs) == limit {
			break
		}
	}
	return svs, nil
}

func loadSession(dir, id string) (*sessionView, error) {
	sv := &sessionView{id: id, open: -1, follow: true}
	sv.started, _ = time.Parse("20060102T150405Z", id[:min(len(id), 16)])
	events, err := session.ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	sv.steps = steps(events)
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
	sv.settled, sv.settleErr = st.Settled, st.Error
	sv.sel = max(len(sv.steps)-1, 0)
	return sv, nil
}

// steps pairs each tool call in events with its result.
func steps(events []session.Event) []step {
	var out []step
	calls := map[string]int{}
	for _, e := range events {
		switch e.Type {
		case "tool":
			summary, input := e.Summary, e.Detail
			if summary == "" {
				// Logged before steps had summaries.
				summary, input = e.Text, e.Text
			}
			if e.ID != "" {
				calls[e.ID] = len(out)
			}
			out = append(out, step{at: e.Time, kind: e.Type, summary: summary, input: input})
		case "result":
			if i, ok := calls[e.ID]; ok {
				out[i].output, out[i].failed, out[i].done = e.Detail, e.Failed, true
				out[i].took = e.Time.Sub(out[i].at)
			}
		case session.EventGate:
			out = append(out, step{at: e.Time, kind: e.Type, summary: e.Summary, output: e.Detail,
				failed: e.Failed, done: true})
		case session.EventSettle:
			out = append(out, step{at: e.Time, kind: e.Type, summary: e.Summary})
		default:
			out = append(out, step{at: e.Time, kind: e.Type, summary: e.Text, input: e.Text})
		}
	}
	return out
}

// latest is what the session is doing now: its last step.
func (sv *sessionView) latest() string {
	if len(sv.steps) == 0 {
		return ""
	}
	return sv.steps[len(sv.steps)-1].summary
}

func (sv *sessionView) running() bool { return !sv.ended && !sv.settled }

// keep carries what was selected and opened in old over to the reread tv.
func (tv *taskView) keep(old *taskView) {
	tv.sel, tv.top = old.sel, old.top
	// New sessions come first, so the one selected moves down.
	if len(tv.sessions) > len(old.sessions) && len(old.sessions) > 0 {
		tv.sel += len(tv.sessions) - len(old.sessions)
	}
	tv.sel = min(tv.sel, max(len(tv.sessions)-1, 0))
	if old.open == nil {
		return
	}
	for _, sv := range tv.sessions {
		if sv.id != old.open.id {
			continue
		}
		o := old.open
		sv.top, sv.follow, sv.open, sv.scroll = o.top, o.follow, o.open, o.scroll
		sv.sel = o.sel
		if o.follow {
			sv.sel = max(len(sv.steps)-1, 0)
		}
		tv.open = sv
	}
}

// update handles a key while the task is open, and reports whether it backs
// out of the task.
func (tv *taskView) update(s *Status, key string) bool {
	if sv := tv.open; sv != nil {
		if sv.update(key) {
			tv.open = nil
		}
		return false
	}
	switch key {
	case "esc", "left", "h":
		return true
	case "j", "down":
		tv.sel = min(tv.sel+1, max(len(tv.sessions)-1, 0))
	case "k", "up":
		tv.sel = max(tv.sel-1, 0)
	case "enter", "space", " ", "right", "l":
		if tv.sel < len(tv.sessions) {
			tv.open = tv.sessions[tv.sel]
		}
	}
	s.confirm = ""
	return false
}

// update handles a key while the session is open, and reports whether it
// backs out of the session.
func (sv *sessionView) update(key string) bool {
	if sv.open >= 0 {
		switch key {
		case "esc", "left", "h":
			sv.open = -1
		case "j", "down":
			sv.scroll++
		case "k", "up":
			sv.scroll = max(sv.scroll-1, 0)
		case "pgdown", "space", " ":
			sv.scroll += 10
		case "pgup":
			sv.scroll = max(sv.scroll-10, 0)
		}
		return false
	}
	switch key {
	case "esc", "left", "h":
		return true
	case "j", "down":
		sv.sel = min(sv.sel+1, max(len(sv.steps)-1, 0))
	case "k", "up":
		sv.sel = max(sv.sel-1, 0)
	case "G", "end":
		sv.sel = max(len(sv.steps)-1, 0)
	case "g", "home":
		sv.sel = 0
	case "enter", "space", " ", "right", "l":
		if sv.sel < len(sv.steps) {
			sv.open, sv.scroll = sv.sel, 0
		}
	}
	sv.follow = sv.sel == len(sv.steps)-1
	return false
}

// crumbs names what is open inside the goal, for the header.
func (tv *taskView) crumbs() string {
	c := tui.Dim(" › ") + tui.Bold(tv.id) + " " + tv.task.Title
	if sv := tv.open; sv != nil {
		c += tui.Dim(" › ") + "Claude " + sv.started.Local().Format("15:04")
		if sv.open >= 0 && sv.open < len(sv.steps) {
			c += tui.Dim(" › ") + sv.steps[sv.open].at.Local().Format("15:04:05")
		}
	}
	return c
}

// render shows the open task, session or step in room lines.
func (tv *taskView) render(s *Status, room int) string {
	if sv := tv.open; sv != nil {
		if sv.open >= 0 && sv.open < len(sv.steps) {
			return sv.renderStep(sv.steps[sv.open], s.width, room)
		}
		return sv.render(s.width, room)
	}
	t := tv.task
	head := fmt.Sprintf("%s %s · %s", stateMark(t.State), t.Kind, t.State)
	if t.Profile != "" {
		head += tui.Dim(" · " + t.Profile)
	}
	lines := []string{head}
	for i, d := range tv.deps {
		if i == 0 {
			lines = append(lines, tui.Dim("depends on:"))
		}
		ws := ""
		if d.Workstream != "" {
			ws = tui.Dim("[" + d.Workstream + "] ")
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s%s %s", stateMark(d.State), d.ID, ws,
			oneLine(d.Title, s.width-24), tui.Dim(string(d.State))))
	}
	lines = append(lines, "")
	sel := 0
	if len(tv.sessions) == 0 {
		lines = append(lines, tui.Dim("No session has worked on it yet."))
	}
	for i, sv := range tv.sessions {
		mark := "  "
		if i == tv.sel {
			mark, sel = tui.Color("› ", tui.Cyan), len(lines)
		}
		lines = append(lines, mark+sv.line(s.width-4))
	}
	for i, q := range tv.questions {
		if i == 0 {
			lines = append(lines, "", tui.Dim("─── questions"))
		}
		state := tui.Color("open", tui.Magenta)
		if q.Answer != "" {
			state = tui.Dim("answered")
		}
		lines = append(lines, fmt.Sprintf("? %s %s", oneLine(q.Text, s.width-14), state))
	}
	if body := strings.TrimSpace(t.Body); body != "" {
		lines = append(lines, "", tui.Dim("─── task"))
		lines = append(lines, strings.Split(ansi.Wordwrap(body, max(s.width, 20), ""), "\n")...)
	}
	return scroll(lines, sel, sel, &tv.top, room)
}

// line is the session as one of a task's: whether Claude is running or how
// it ended, and what diatom made of it.
func (sv *sessionView) line(width int) string {
	at := sv.started.Local().Format("Jan 2 15:04")
	steps := fmt.Sprintf("%d steps", len(sv.steps))
	var line string
	switch {
	case sv.running():
		line = tui.Color(
			"▶ Claude running",
			tui.Green,
		) + tui.Dim(
			fmt.Sprintf(" for %s · %s · started %s",
				time.Since(sv.started).Round(time.Second), steps, at),
		)
		if l := sv.latest(); l != "" {
			line += tui.Dim(" · ") + oneLine(l, width/3)
		}
		return line
	case !sv.settled && sv.err == "":
		// Claude is done, and diatom is checking and committing its work.
		line = tui.Color(
			"▶ diatom wrapping up",
			tui.Green,
		) + tui.Dim(
			fmt.Sprintf(" · Claude %s · %s · started %s",
				sv.outcome, steps, at),
		)
		if l := sv.latest(); l != "" {
			line += tui.Dim(" · ") + oneLine(l, width/3)
		}
		return line
	case sv.err != "":
		line = tui.Color("✗ Claude failed", tui.Red) + tui.Dim(" · "+steps+" · "+at)
	case sv.settleErr != "":
		line = tui.Color(
			"✗ Claude "+sv.outcome,
			tui.Red,
		) + tui.Dim(
			fmt.Sprintf(" · %s · $%.2f · %s",
				steps, sv.costUSD, at),
		)
	case !sv.ended:
		line = tui.Dim("◌ Claude stopped · " + steps + " · " + at)
	default:
		line = tui.Dim(
			"✓ ",
		) + "Claude " + sv.outcome + tui.Dim(
			fmt.Sprintf(" · %s · %d turns · $%.2f · %s",
				steps, sv.turns, sv.costUSD, at),
		)
	}
	if why := sv.settleErr + sv.err; why != "" {
		line += tui.Color(" · "+oneLine(why, width/3), tui.Red)
	}
	return line
}

// render lists the session's steps, the latest last, following them while
// the session runs.
func (sv *sessionView) render(width, room int) string {
	lines := []string{sv.line(width), tui.Dim("session " + sv.id)}
	if sv.settleErr != "" {
		lines = append(lines, tui.Color("diatom couldn't settle its work: "+sv.settleErr, tui.Red))
	}
	if sv.err != "" {
		lines = append(lines, tui.Color("the session failed: "+sv.err, tui.Red))
	}
	lines = append(lines, "")
	sel := len(lines)
	if len(sv.steps) == 0 {
		lines = append(lines, tui.Dim("No steps yet."))
	}
	for i, st := range sv.steps {
		mark := "  "
		if i == sv.sel {
			mark, sel = tui.Color("› ", tui.Cyan), len(lines)
		}
		lines = append(lines, mark+tui.Dim(st.at.Local().Format("15:04:05"))+" "+
			st.line(width-14, !sv.settled && i == len(sv.steps)-1))
	}
	return scroll(lines, sel, sel, &sv.top, room)
}

// line is a step as one of a session's: a mark for how it went and its
// summary.
func (st step) line(width int, last bool) string {
	text := oneLine(st.summary, width)
	switch {
	case st.kind == "text":
		return tui.Dim("“ " + text)
	case st.failed:
		return tui.Color("✗ "+text, tui.Red)
	case st.kind == session.EventGate:
		return tui.Color("✓ "+text, tui.Green)
	case st.kind == session.EventSettle && last:
		return tui.Color("▶ ", tui.Green) + tui.Dim("diatom: ") + text
	case st.kind == session.EventSettle:
		return tui.Dim("✓ diatom: " + text)
	case !st.done && last:
		return tui.Color("▶ "+text, tui.Green)
	case !st.done:
		return tui.Dim("· ") + text
	}
	took := ""
	if st.took >= time.Second {
		took = tui.Dim(" · " + st.took.Round(time.Second).String())
	}
	return tui.Dim("✓ ") + text + took
}

// renderStep shows one step in full: the command or words, then the output,
// scrolled.
func (sv *sessionView) renderStep(st step, width, room int) string {
	wrap := func(s string) []string {
		return strings.Split(ansi.Wordwrap(strings.TrimRight(s, "\n"), max(width, 20), ""), "\n")
	}
	lines := append([]string{st.line(width, false), ""}, wrap(st.input)...)
	if st.kind == "text" {
		lines = wrap(st.input)
	}
	if st.output != "" {
		head := "─── output"
		if st.took >= time.Second {
			head += " after " + st.took.Round(time.Second).String()
		}
		lines = append(lines, "", tui.Dim(head))
		lines = append(lines, wrap(st.output)...)
	} else if st.kind == "tool" && !st.done && sv.running() && sv.open == len(sv.steps)-1 {
		lines = append(lines, "", tui.Dim("─── still running"))
	}
	sv.scroll = min(sv.scroll, max(len(lines)-room, 0))
	shown := lines[sv.scroll:min(sv.scroll+room, len(lines))]
	if sv.scroll > 0 {
		shown = append([]string{tui.Dim("↑ more above")}, shown[1:]...)
	}
	if sv.scroll+room < len(lines) {
		shown = append(shown[:len(shown)-1], tui.Dim("↓ more below"))
	}
	return strings.Join(shown, "\n")
}
