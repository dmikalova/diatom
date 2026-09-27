package spend

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// ended writes a session of goal's that ended at, costing usd and commit.
func ended(t *testing.T, s *queue.Store, goal, id string, at time.Time, usd, commit float64,
	settled bool,
) string {
	t.Helper()
	dir := filepath.Join(s.SessionsDir(goal), id)
	if err := session.Create(
		dir,
		session.Spec{ID: id, Tasks: []string{"0001", "0002"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := session.UpdateState(dir, func(st *session.State) {
		st.CommitCostUSD, st.Settled = commit, settled
	}); err != nil {
		t.Fatal(err)
	}
	res := map[string]any{"usage": map[string]any{"costUSD": usd}}
	if err := session.WriteResult(dir, res); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "result.json"), at, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRepo(t *testing.T) {
	s := queue.Open(t.TempDir())
	now := time.Date(2026, 9, 27, 15, 0, 0, 0, time.Local)
	ended(t, s, "set", "a", now.Add(-time.Hour), 10, 0.5, true)
	ended(t, s, "set", "b", now.Add(-16*time.Hour), 20, 0, true) // yesterday
	ended(t, s, "done", "c", now.AddDate(0, 0, -6), 40, 0, true) // a finished goal's
	ended(t, s, "done", "d", now.AddDate(0, 0, -20), 80, 0, true)
	ended(t, s, "_intake", "e", now.AddDate(0, 0, -29), 160, 0, true)
	ended(t, s, "set", "f", now.AddDate(0, 0, -31), 320, 0, true) // dropped off
	// Still running: nothing yet.
	if err := session.Create(filepath.Join(s.SessionsDir("set"), "g"), session.Spec{}); err != nil {
		t.Fatal(err)
	}
	tally := New()
	got := tally.Repo(s, now)
	if want := (Totals{Day: 10.5, Week: 70.5, Month: 310.5}); got != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
	if over := got.Over(config.Budget{}); over != "" {
		t.Errorf("no budget is spent as %q", over)
	}
	if over := got.Over(config.Budget{Day: 50, Week: 70, Month: 1000}); over != "this week's" {
		t.Errorf("over = %q", over)
	}
	sc := got.Scales(config.Budget{Day: 10})
	if !sc[0].Over() || sc[1].Over() || sc[0].Letter != "D" || sc[2].Letter != "M" {
		t.Errorf("scales = %+v", sc)
	}
}

// usd is what a session cost, to the cent.
func usd(c Session) string { return fmt.Sprintf("%.2f", c.USD) }

func TestSessionCache(t *testing.T) {
	s := queue.Open(t.TempDir())
	now := time.Now()
	settled := ended(t, s, "set", "a", now, 1, 0, true)
	open := ended(t, s, "set", "b", now, 2, 0, false)
	tally := New()
	if c := tally.Goal(s, "set"); len(c) != 2 || usd(c[0]) != "1.00" || usd(c[1]) != "2.00" ||
		len(c[0].Tasks) != 2 {
		t.Fatalf("goal = %+v", c)
	}
	// A settled session's cost is kept; one still settling is read again.
	for _, dir := range []string{settled, open} {
		res := map[string]any{"usage": map[string]any{"costUSD": 5}}
		if err := session.WriteResult(dir, res); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := tally.Session(settled); usd(c) != "1.00" {
		t.Errorf("settled = %v, want the kept 1", c.USD)
	}
	if c, _ := tally.Session(open); usd(c) != "5.00" {
		t.Errorf("settling = %v, want 5 read again", c.USD)
	}
	if _, ok := tally.Session(filepath.Join(s.SessionsDir("set"), "none")); ok {
		t.Error("a missing session has a cost")
	}
}

func TestSpreadOverTheDaysWorked(t *testing.T) {
	s := queue.Open(t.TempDir())
	now := time.Date(2026, 9, 27, 15, 0, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1)
	// A run stopped yesterday evening, and resumed today: 3 steps
	// yesterday, then one yesterday and three today.
	dir := ended(t, s, "set", "a", now.Add(-time.Hour), 8, 0, true)
	if err := session.UpdateState(dir, func(st *session.State) {
		st.Earlier = []session.Run{{Ended: yesterday.Add(time.Hour), CostUSD: 6}}
	}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []session.Event{
		{Time: yesterday, Type: "tool"}, {Time: yesterday.Add(time.Minute), Type: "text"},
		{Time: yesterday.Add(2 * time.Minute), Type: "tool"},
		{Time: yesterday.Add(3 * time.Hour), Type: "text"},
		{Time: now.Add(-3 * time.Hour), Type: "tool"}, {Time: now.Add(-2 * time.Hour), Type: "tool"},
		// Gate runs and diatom's own steps aren't the agent's.
		{Time: now.Add(-90 * time.Minute), Type: session.EventGate},
		{Time: now.Add(-80 * time.Minute), Type: session.EventSettle},
		{Time: now.Add(-70 * time.Minute), Type: "tool"},
		{Type: "tool"},
	} {
		if err := session.AppendEvent(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	got := New().Repo(s, now)
	if fmt.Sprintf("%.2f %.2f", got.Day, got.Week) != "6.00 14.00" {
		t.Errorf("totals = %+v, want 6 today of 14", got)
	}
}

func TestSuspendedRunsCount(t *testing.T) {
	s := queue.Open(t.TempDir())
	now := time.Now()
	dir := filepath.Join(s.SessionsDir("set"), "a")
	if err := session.Create(dir, session.Spec{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	tally := New()
	if _, ok := tally.Session(dir); ok {
		t.Error("a session that spent nothing yet has a cost")
	}
	if err := session.UpdateState(dir, func(st *session.State) {
		st.Earlier = []session.Run{{Ended: now, CostUSD: 3}}
	}); err != nil {
		t.Fatal(err)
	}
	if c, ok := tally.Session(dir); !ok || usd(c) != "3.00" || c.Ended {
		t.Errorf("a resumed session running = %+v, %v", c, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := tally.Session(dir); ok {
		t.Error("a broken state has a cost")
	}
}
