// Package spend adds up what a repo's agent sessions have cost: each goal's,
// and the whole repo's over today, the last 7 days and the last 30, which the
// repo's budget caps. A session counts from the moment its agent ended, for
// as long as its directory is kept, the goal's finishing included.
package spend

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// Session is what one ended session cost, when it ended, and the tasks it
// worked on.
type Session struct {
	USD   float64
	At    time.Time
	Tasks []string
}

// Tally reads what sessions cost, keeping what each settled one cost: that
// never changes.
type Tally struct {
	mu      sync.Mutex
	settled map[string]Session
}

// New returns an empty tally.
func New() *Tally { return &Tally{settled: map[string]Session{}} }

// Session is what the session in dir cost; false while its agent runs. One
// stopped and resumed counts what it cost after its last start.
func (t *Tally) Session(dir string) (Session, bool) {
	t.mu.Lock()
	c, ok := t.settled[dir]
	t.mu.Unlock()
	if ok {
		return c, true
	}
	at, ended := session.Ended(dir)
	if !ended {
		return Session{}, false
	}
	spec, err := session.Load(dir)
	if err != nil {
		return Session{}, false
	}
	var res struct {
		Usage struct {
			CostUSD float64 `json:"costUSD"`
		} `json:"usage"`
	}
	if ok, err := session.ReadResult(dir, &res); err != nil || !ok {
		return Session{}, false
	}
	c = Session{USD: res.Usage.CostUSD, At: at, Tasks: spec.Tasks}
	if st, err := session.LoadState(dir); err == nil {
		c.USD += st.CommitCostUSD
		if st.Settled {
			t.mu.Lock()
			t.settled[dir] = c
			t.mu.Unlock()
		}
	}
	return c, true
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
	entries, _ := os.ReadDir(filepath.Join(s.Root, "goals"))
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	week, month := midnight.AddDate(0, 0, -6), midnight.AddDate(0, 0, -29)
	var tot Totals
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, c := range t.Goal(s, e.Name()) {
			if c.At.Before(month) {
				continue
			}
			tot.Month += c.USD
			if !c.At.Before(week) {
				tot.Week += c.USD
			}
			if !c.At.Before(midnight) {
				tot.Day += c.USD
			}
		}
	}
	return tot
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
