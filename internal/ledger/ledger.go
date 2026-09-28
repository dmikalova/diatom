// Package ledger keeps, for good, what each landed goal cost and what it
// landed: the lines its final diff added and removed, by code, tests and
// docs, and what its sessions spent. Sessions are cleared with their goals,
// so this is what shows how lines per dollar change over months, across
// every repo.
package ledger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/spend"
)

// FileName is the ledger, one landed goal a line, in diatom's state
// directory.
const FileName = "landed.jsonl"

// Path is the ledger's file.
func Path(p config.Paths) string { return filepath.Join(p.StateDir(), FileName) }

// Lines are the lines a diff added and removed.
type Lines struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// Landed is one landed goal: its final diff, from the base it was laid out
// on to the tip that merged, and what its sessions cost.
type Landed struct {
	Repo     string    `json:"repo"`
	Goal     string    `json:"goal"`
	Title    string    `json:"title,omitempty"`
	Finished time.Time `json:"finished"`
	Base     string    `json:"base"`
	Tip      string    `json:"tip"`
	Files    int       `json:"files"`
	// Code, Tests and Docs split the diff's lines by the kind of file: a
	// test file, a Markdown file, or any other.
	Code  Lines `json:"code"`
	Tests Lines `json:"tests"`
	Docs  Lines `json:"docs"`
	// CostUSD and Sessions are what the goal's sessions spent, and how many
	// there were, as kept when it finished.
	CostUSD  float64 `json:"costUSD"`
	Sessions int     `json:"sessions"`
}

// Added is every line the goal added.
func (l Landed) Added() int { return l.Code.Added + l.Tests.Added + l.Docs.Added }

// LOC is the lines of code the goal landed: those it added to code and test
// files, not docs.
func (l Landed) LOC() int { return l.Code.Added + l.Tests.Added }

// Period is what a stretch of landed goals came to.
type Period struct {
	// Start is the period's first day, a Monday for a week.
	Start   time.Time
	Goals   int
	LOC     int
	Docs    int
	CostUSD float64
}

// PerDollar is the period's lines of code for each dollar spent, 0 with
// nothing spent.
func (p Period) PerDollar() float64 {
	if p.CostUSD <= 0 {
		return 0
	}
	return float64(p.LOC) / p.CostUSD
}

// Sum adds up landed goals.
func Sum(ls []Landed) Period {
	var p Period
	for _, l := range ls {
		p.Goals++
		p.LOC += l.LOC()
		p.Docs += l.Docs.Added
		p.CostUSD += l.CostUSD
	}
	return p
}

// Weeks groups landed goals by the week, Monday to Sunday in local time,
// they finished in, the latest first.
func Weeks(ls []Landed) []Period {
	byWeek := map[int64][]Landed{}
	for _, l := range ls {
		t := l.Finished.Local()
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
		start := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		byWeek[start.Unix()] = append(byWeek[start.Unix()], l)
	}
	out := make([]Period, 0, len(byWeek))
	for start, week := range byWeek {
		p := Sum(week)
		p.Start = time.Unix(start, 0)
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Period) int { return b.Start.Compare(a.Start) })
	return out
}

// ErrNoLayout is a finished goal with no layout to measure, such as one
// finished by hand.
var ErrNoLayout = errors.New("the goal has no layout to measure")

// mu keeps two writers in one process from both appending a goal.
var mu sync.Mutex

// Load reads the ledger, oldest first; a missing one is empty.
func Load(path string) ([]Landed, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Landed
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var l Landed
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

// Record appends a landed goal to the ledger, unless it is there already.
func Record(path string, l Landed) error {
	mu.Lock()
	defer mu.Unlock()
	have, err := Load(path)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(
		have,
		func(h Landed) bool { return h.Repo == l.Repo && h.Goal == l.Goal },
	) {
		return nil
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Measure is what a finished goal landed and cost: its final diff, from its
// layout's base to the tip that merged, and its sessions' cost.
func Measure(
	ctx context.Context,
	s *queue.Store,
	tally *spend.Tally,
	g *queue.Goal,
) (Landed, error) {
	l := Landed{Repo: s.Repo(), Goal: g.Name, Title: g.Title, Finished: g.Finished}
	res, err := finish.Load(s.GoalDir(g.Name))
	if err != nil {
		return l, err
	}
	if res == nil || len(res.Stack) == 0 {
		return l, ErrNoLayout
	}
	l.Base, l.Tip = res.Base, res.Tip()
	out, err := git.Repo{
		Dir: s.Repo(),
	}.Output(
		ctx,
		"diff",
		"--numstat",
		"--no-renames",
		l.Base,
		l.Tip,
	)
	if err != nil {
		return l, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 {
			continue
		}
		l.Files++
		added, aerr := strconv.Atoi(f[0])
		removed, rerr := strconv.Atoi(f[1])
		if aerr != nil || rerr != nil {
			// A binary file counts no lines.
			continue
		}
		kind := &l.Code
		switch path := f[2]; {
		case isTest(path):
			kind = &l.Tests
		case strings.EqualFold(filepath.Ext(path), ".md"):
			kind = &l.Docs
		}
		kind.Added += added
		kind.Removed += removed
	}
	for _, c := range tally.Goal(s, g.Name) {
		l.CostUSD += c.USD
		l.Sessions++
	}
	return l, nil
}

// isTest reports whether path is a test file, in the naming of Go, and of
// JavaScript and TypeScript.
func isTest(path string) bool {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return strings.HasSuffix(name, "_test") || strings.HasSuffix(name, ".test") ||
		strings.HasSuffix(name, ".spec") || strings.HasPrefix(name, "test_")
}

// Backfill records every finished goal of the repo the ledger lacks, such as
// those that finished before diatom kept it, and returns how many it added.
func Backfill(ctx context.Context, path string, s *queue.Store, tally *spend.Tally) (int, error) {
	have, err := Load(path)
	if err != nil {
		return 0, err
	}
	goals, err := s.Goals()
	if err != nil {
		return 0, err
	}
	added := 0
	var errs []error
	for _, g := range goals {
		if g.State != queue.GoalFinished || slices.ContainsFunc(have, func(h Landed) bool {
			return h.Repo == s.Repo() && h.Goal == g.Name
		}) {
			continue
		}
		l, err := Measure(ctx, s, tally, g)
		if errors.Is(err, ErrNoLayout) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := Record(path, l); err != nil {
			return added, err
		}
		added++
	}
	return added, errors.Join(errs...)
}
