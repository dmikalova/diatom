package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/ledger"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/spend"
	"github.com/dmikalova/diatom/internal/tui"
)

// renderSpending shows what the sessions spent on each of the last 30 days,
// against the budget, or the day selected opened to its goals.
func (a *App) renderSpending(w, h int) string {
	days := a.status.days
	a.spendSel = max(min(a.spendSel, len(days)-1), 0)
	if a.spendOpen && len(days) > 0 {
		return a.renderSpendDay(days[a.spendSel], w, h)
	}
	head := []string{
		tui.Bold("Spending") + tui.Dim(" · the last 30 days · j and k choose a day · "+
			"enter opens it · esc closes"),
		a.budgetLine(),
		a.totalsLine(),
		"",
	}
	head = append(head, a.landedLines(w)...)
	if len(days) == 0 {
		return strings.Join(append(head, tui.Dim("Nothing spent in the last 30 days.")), "\n")
	}
	most := 0.0
	for _, d := range days {
		most = max(most, d.USD)
	}
	today := days[0].Date.Equal(midnightOf(a.env.now()))
	var rows []string
	for i, d := range days {
		label := d.Date.Format("Mon Jan 2")
		if i == 0 && today {
			label += " · today"
		}
		row := fmt.Sprintf("%-18s %9s ", label, fmt.Sprintf("$%.2f", d.USD))
		bar := strings.Repeat("█", max(int(float64(max(w-32, 1))*d.USD/max(most, 0.01)), 1))
		if limit := a.status.budget.Day; limit > 0 && d.USD >= limit {
			row, bar = tui.Color(row, tui.Red), tui.Color(bar, tui.Red)
		} else {
			bar = tui.Color(bar, tui.Accent)
		}
		lead := " "
		if i == a.spendSel {
			lead, row = tui.Color("▌", tui.Accent), tui.Bold(row)
		}
		rows = append(rows, lead+row+bar)
	}
	room := max(h-len(head), 1)
	top := max(a.spendSel-room+1, 0)
	return strings.Join(append(head, rows[top:min(top+room, len(rows))]...), "\n")
}

// budgetLine says what the budget caps, or how to set one.
func (a *App) budgetLine() string {
	b := a.status.budget
	var caps []string
	for _, c := range []struct {
		usd float64
		per string
	}{{b.Day, "a day"}, {b.Week, "a week"}, {b.Month, "a month"}} {
		if c.usd > 0 {
			caps = append(caps, money(c.usd)+" "+c.per)
		}
	}
	if len(caps) == 0 {
		return tui.Dim("No budget: a [budget] table in the config caps the day, week or month.")
	}
	return "Budget: " + strings.Join(caps, " · ")
}

// totalsLine is what was spent over each of the budget's scales, in red
// where that spends it.
func (a *App) totalsLine() string {
	names := map[string]string{"D": "today", "W": "the last 7 days", "M": "the last 30"}
	var parts []string
	for _, sc := range a.status.totals.Scales(a.status.budget) {
		t := fmt.Sprintf("%s $%.2f", names[sc.Letter], sc.Spent)
		if sc.Over() {
			t = tui.Color(t, tui.Red)
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, tui.Dim(" · "))
}

// landedWeeks is how many weeks the landed lines show.
const landedWeeks = 6

// landedLines show what the workspace's landed goals came to, from the
// ledger that outlasts their sessions: lines of code, those added to code
// and test files, for each dollar their sessions spent, over all time and
// for each of the last weeks.
func (a *App) landedLines(w int) []string {
	all, err := ledger.Load(ledger.Path(a.env.Paths))
	if err != nil {
		return []string{tui.Color("the ledger of landed goals: "+err.Error(), tui.Red), ""}
	}
	repos := map[string]bool{}
	for _, s := range a.env.stores() {
		repos[s.Repo()] = true
	}
	all = slices.DeleteFunc(all, func(l ledger.Landed) bool { return !repos[l.Repo] })
	if len(all) == 0 {
		return nil
	}
	sum := ledger.Sum(all)
	lines := []string{
		tui.Bold("Landed") + tui.Dim(" · lines of code for each dollar, as merged"),
		fmt.Sprintf("%s · %s lines of code, %s of docs, for $%.2f · %s",
			count(sum.Goals, "goal"), thousands(sum.LOC), thousands(sum.Docs), sum.CostUSD,
			tui.Bold(fmt.Sprintf("%.0f lines/$", sum.PerDollar()))),
	}
	weeks := ledger.Weeks(all)
	best := 0.0
	for _, p := range weeks {
		best = max(best, p.PerDollar())
	}
	for _, p := range weeks[:min(len(weeks), landedWeeks)] {
		row := fmt.Sprintf("  week of %-7s %9s %9s %7s ", p.Start.Format("Jan 2"),
			fmt.Sprintf("$%.2f", p.CostUSD), thousands(p.LOC), fmt.Sprintf("%.0f/$", p.PerDollar()))
		bar := strings.Repeat("█", max(int(float64(max(w-44, 1))*p.PerDollar()/max(best, 1)), 1))
		lines = append(lines, row+tui.Color(bar, tui.Green))
	}
	return append(lines, "")
}

// thousands writes n with a comma between each three digits.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// renderSpendDay shows what each goal's sessions spent on a day, the most
// first, scrolled down spendTop lines.
func (a *App) renderSpendDay(d spend.Day, w, h int) string {
	sessions := 0
	for _, g := range d.Goals {
		sessions += len(g.Sessions)
	}
	head := []string{
		"‹ " + tui.Bold(
			d.Date.Format("Monday, Jan 2"),
		) + tui.Dim(
			fmt.Sprintf(" · $%.2f · %s · esc "+
				"goes back", d.USD, count(sessions, "session")),
		),
		"",
	}
	var lines []string
	for _, g := range d.Goals {
		lines = append(lines, tui.Bold(a.goalTitle(g))+"  "+fmt.Sprintf("$%.2f", g.USD))
		for _, s := range g.Sessions {
			lines = append(lines, "  "+ansi.Truncate(a.sessionLine(g, s), max(w-2, 10), "…"))
		}
		lines = append(lines, "")
	}
	room := max(h-len(head), 1)
	a.spendTop = max(min(a.spendTop, len(lines)-room), 0)
	return strings.Join(append(head, lines[a.spendTop:min(a.spendTop+room, len(lines))]...), "\n")
}

// spendStore is the store of the repo a day's goal is in.
func (a *App) spendStore(g spend.GoalDay) *queue.Store {
	for _, s := range a.env.stores() {
		if s.Repo() == g.Repo {
			return s
		}
	}
	return a.env.Store
}

// goalTitle is a goal's title, or its name when it has none, named with its
// repo over a workspace.
func (a *App) goalTitle(g spend.GoalDay) string {
	title := g.Goal
	if g.Goal == queue.IntakeGoal {
		title = "Triage"
	} else if goal, err := a.spendStore(g).Goal(g.Goal); err == nil && goal.Title != "" {
		title = goal.Title
	}
	if a.env.many() {
		return filepath.Base(g.Repo) + " · " + title
	}
	return title
}

// sessionLine says what a session was and what it spent on the day: its
// workstream, its kind, and the tasks it worked on.
func (a *App) sessionLine(g spend.GoalDay, s spend.SessionDay) string {
	ws := s.Session.Workstream
	if ws == "" {
		ws = "planning"
	}
	parts := []string{ws}
	if s.Session.Kind != "" {
		parts = append(parts, string(s.Session.Kind))
	}
	parts = append(parts, fmt.Sprintf("$%.2f", s.USD))
	store := a.spendStore(g)
	var tasks []string
	for _, id := range s.Session.Tasks {
		if t, err := store.Task(g.Goal, id); err == nil {
			tasks = append(tasks, id+" "+t.Title)
		} else {
			tasks = append(tasks, id)
		}
	}
	line := strings.Join(parts, tui.Dim(" · "))
	if len(tasks) > 0 {
		line += tui.Dim(" · " + strings.Join(tasks, ", "))
	}
	return line
}

// spendingKey chooses and opens a day, or scrolls the day opened, and
// reports whether the key goes back to the nav.
func (a *App) spendingKey(key string) bool {
	room := max(a.height-3, 1)
	if a.spendOpen {
		switch key {
		case "esc", "left", "h":
			a.spendOpen, a.spendTop = false, 0
		case "j", "down":
			a.spendTop++
		case "k", "up":
			a.spendTop = max(a.spendTop-1, 0)
		case "pgdown", "space", " ":
			a.spendTop += room / 2
		case "pgup", "shift+space":
			a.spendTop = max(a.spendTop-room/2, 0)
		}
		return false
	}
	switch key {
	case "esc", "left":
		return true
	case "j", "down":
		a.spendSel = min(a.spendSel+1, max(len(a.status.days)-1, 0))
	case "k", "up":
		a.spendSel = max(a.spendSel-1, 0)
	case "g", "home":
		a.spendSel = 0
	case "enter", "space", " ", "right", "l":
		a.spendOpen = len(a.status.days) > 0
	}
	return false
}

// midnightOf is the start of t's day, where t is.
func midnightOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
