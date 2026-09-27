// Package spend adds up what a repo's agent sessions have cost: each goal's,
// and the whole repo's over today, the last 7 days and the last 30, which the
// repo's budget caps. Each run of an agent counts once it has ended, spread
// over the days it worked, for as long as its session is kept, the goal's
// finishing included.
package spend

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// Session is what one session has cost so far, the tasks it worked on, and
// that cost spread over the days it was spent.
type Session struct {
	// ID, Workstream and Kind are the session's, from its spec.
	ID, Workstream string
	Kind           queue.Kind
	USD            float64
	Tasks          []string
	Parts          []Part
	// Ended is set once the agent's last run has ended.
	Ended bool
}

// Part is what a session spent on one day, at the last moment it worked
// that day.
type Part struct {
	At  time.Time
	USD float64
}

// Tally reads what sessions cost, keeping what each settled one cost: that
// never changes.
type Tally struct {
	mu      sync.Mutex
	settled map[string]Session
}

// New returns an empty tally.
func New() *Tally { return &Tally{settled: map[string]Session{}} }

// Session is what the session in dir has cost: each run of its agent that
// has ended, and what its commit messages cost. False when nothing has been
// spent yet.
func (t *Tally) Session(dir string) (Session, bool) {
	t.mu.Lock()
	c, ok := t.settled[dir]
	t.mu.Unlock()
	if ok {
		return c, true
	}
	spec, err := session.Load(dir)
	if err != nil {
		return Session{}, false
	}
	st, err := session.LoadState(dir)
	if err != nil {
		return Session{}, false
	}
	runs := st.Earlier
	at, ended := session.Ended(dir)
	if ended {
		var res struct {
			Usage struct {
				CostUSD float64 `json:"costUSD"`
			} `json:"usage"`
		}
		if ok, err := session.ReadResult(dir, &res); err != nil || !ok {
			return Session{}, false
		}
		runs = append(runs, session.Run{Ended: at, CostUSD: res.Usage.CostUSD + st.CommitCostUSD})
	}
	if len(runs) == 0 {
		return Session{}, false
	}
	events, _ := session.ReadEvents(dir)
	c = Session{
		ID: filepath.Base(dir), Workstream: spec.Workstream, Kind: spec.Kind,
		Tasks: spec.Tasks, Parts: spread(runs, events), Ended: ended,
	}
	for _, r := range runs {
		c.USD += r.CostUSD
	}
	if ended && st.Settled {
		t.mu.Lock()
		t.settled[dir] = c
		t.mu.Unlock()
	}
	return c, true
}

// spread shares each run's cost among the days its agent worked, by how many
// of its steps fell on each: a run that goes on past midnight, or is resumed
// the next morning, is spent on both days.
func spread(runs []session.Run, events []session.Event) []Part {
	var parts []Part
	var from time.Time
	for _, r := range runs {
		// The days of the run's steps, in order, with the count and last
		// moment of each.
		var days []Part
		steps := 0
		for _, e := range events {
			if e.Time.IsZero() || !e.Time.After(from) || e.Time.After(r.Ended) ||
				e.Type == session.EventGate || e.Type == session.EventSettle {
				continue
			}
			steps++
			if n := len(days); n > 0 && sameDay(days[n-1].At, e.Time) {
				days[n-1].At = e.Time
				days[n-1].USD++
				continue
			}
			days = append(days, Part{At: e.Time, USD: 1})
		}
		if steps == 0 {
			days = []Part{{At: r.Ended, USD: 1}}
			steps = 1
		}
		for _, d := range days {
			parts = append(parts, Part{At: d.At, USD: r.CostUSD * d.USD / float64(steps)})
		}
		from = r.Ended
	}
	return parts
}

// sameDay reports whether a and b fall on the same local day.
func sameDay(a, b time.Time) bool {
	a, b = a.Local(), b.Local()
	return a.YearDay() == b.YearDay() && a.Year() == b.Year()
}

// Goal is what each of a goal's ended sessions cost.
func (t *Tally) Goal(s *queue.Store, goal string) []Session {
	dirs, _ := filepath.Glob(filepath.Join(s.SessionsDir(goal), "*"))
	var out []Session
	for _, dir := range dirs {
		if c, ok := t.Session(dir); ok {
			out = append(out, c)
		}
	}
	return out
}

// Totals is what the repo's sessions cost today, over the last 7 days and
// over the last 30, each counted in whole days back from midnight.
type Totals struct{ Day, Week, Month float64 }

// Repo adds up what every goal's sessions cost in the days up to now: the
// finished goals', and triage's, too.
func (t *Tally) Repo(s *queue.Store, now time.Time) Totals {
	return Sum(t.Days(s, now), now)
}

// Sum adds up days into what was spent today, over the last 7 days and over
// the last 30.
func Sum(days []Day, now time.Time) Totals {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	week := midnight.AddDate(0, 0, -6)
	var tot Totals
	for _, d := range days {
		tot.Month += d.USD
		if !d.Date.Before(week) {
			tot.Week += d.USD
		}
		if d.Date.Equal(midnight) {
			tot.Day += d.USD
		}
	}
	return tot
}

// Day is what the repo's sessions spent on one day, from its local
// midnight, and what each goal's spent, the most first.
type Day struct {
	Date  time.Time
	USD   float64
	Goals []GoalDay
}

// GoalDay is what one goal's sessions spent on a day, and each session's,
// the most first.
type GoalDay struct {
	Goal     string
	USD      float64
	Sessions []SessionDay
}

// SessionDay is what one session spent on a day.
type SessionDay struct {
	Session Session
	USD     float64
}

// Days is what the repo's sessions spent on each of the last 30 days up to
// now that they spent anything, the latest first.
func (t *Tally) Days(s *queue.Store, now time.Time) []Day {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	month := midnight.AddDate(0, 0, -29)
	// Each day is keyed by when its midnight is, in seconds.
	byDay := map[int64]map[string]*GoalDay{}
	entries, _ := os.ReadDir(filepath.Join(s.Root, "goals"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, c := range t.Goal(s, e.Name()) {
			spent := map[int64]float64{}
			for _, p := range c.Parts {
				at := p.At.In(now.Location())
				day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, now.Location())
				if !day.Before(month) && !day.After(midnight) {
					spent[day.Unix()] += p.USD
				}
			}
			for day, usd := range spent {
				goals := byDay[day]
				if goals == nil {
					goals = map[string]*GoalDay{}
					byDay[day] = goals
				}
				g := goals[e.Name()]
				if g == nil {
					g = &GoalDay{Goal: e.Name()}
					goals[e.Name()] = g
				}
				g.USD += usd
				g.Sessions = append(g.Sessions, SessionDay{Session: c, USD: usd})
			}
		}
	}
	days := make([]Day, 0, len(byDay))
	for date, goals := range byDay {
		d := Day{Date: time.Unix(date, 0).In(now.Location())}
		for _, g := range goals {
			slices.SortStableFunc(
				g.Sessions,
				func(a, b SessionDay) int { return cmp.Compare(b.USD, a.USD) },
			)
			d.USD += g.USD
			d.Goals = append(d.Goals, *g)
		}
		slices.SortFunc(d.Goals, func(a, b GoalDay) int {
			return cmp.Or(cmp.Compare(b.USD, a.USD), cmp.Compare(a.Goal, b.Goal))
		})
		days = append(days, d)
	}
	slices.SortFunc(days, func(a, b Day) int { return b.Date.Compare(a.Date) })
	return days
}

// Scale is one of the budget's: the letter the window marks it with, and
// what it caps.
type Scale struct {
	Letter, Name string
	Spent, Cap   float64
}

// Scales are the totals beside the budget's caps, today first.
func (tot Totals) Scales(b config.Budget) []Scale {
	return []Scale{
		{"D", "today's", tot.Day, b.Day},
		{"W", "this week's", tot.Week, b.Week},
		{"M", "this month's", tot.Month, b.Month},
	}
}

// Over reports whether the scale's budget is spent.
func (sc Scale) Over() bool { return sc.Cap > 0 && sc.Spent >= sc.Cap }

// Over names the first scale whose budget is spent, "" when none is.
func (tot Totals) Over(b config.Budget) string {
	for _, sc := range tot.Scales(b) {
		if sc.Over() {
			return sc.Name
		}
	}
	return ""
}
