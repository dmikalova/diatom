package spend

import (
	"fmt"
	"testing"

	"github.com/dmikalova/diatom/internal/session"
)

func callEv(id string, c session.Call) session.Event {
	return session.Event{Type: session.EventCall, ID: id, Call: &c}
}

func doneEv(cmd string) session.Event {
	return session.Event{Type: "tool", Text: "Bash " + cmd, Detail: cmd}
}

// TestShares pins that a session's cost goes to each task by the model calls
// made for it: up to and including the one that reports it done, each call
// once however many parts its message came in, and what follows the last
// report to every task alike.
func TestShares(t *testing.T) {
	events := []session.Event{
		callEv("m1", session.Call{Input: 100, CacheRead: 1000}),
		callEv("m2", session.Call{Input: 100, CacheWrite1h: 50}),
		callEv("m2", session.Call{Input: 100, CacheWrite1h: 50}),
		doneEv(`diatom task done 0001 "Ward is a keyword."`),
		callEv("m3", session.Call{CacheRead: 3000}),
		doneEv("diatom task note 0002 x && diatom task done 0002"),
		callEv("m4", session.Call{Input: 40}),
	}
	got := shares([]string{"0001", "0002"}, events, 0)
	// 0001: m1 (100 + 100) and m2 (100 + 100), 0002: m3 (300), and m4 (40)
	// alike: 420 + 20 and 300 + 20 of 740.
	want := map[string]float64{"0001": 420.0 / 740, "0002": 320.0 / 740}
	for task, w := range want {
		if fmt.Sprintf("%.4f", got[task]) != fmt.Sprintf("%.4f", w) {
			t.Errorf("share of %s = %.4f, want %.4f (all %v)", task, got[task], w, got)
		}
	}
	if shares([]string{"0001"}, []session.Event{doneEv("diatom task done 0001")}, 0) != nil {
		t.Error("a session with no calls has shares")
	}
}

// TestSharesCountOutput pins that what a call wrote counts at the price of
// output, in the session's own tokens per character when its result has
// them.
func TestSharesCountOutput(t *testing.T) {
	events := []session.Event{
		callEv("m1", session.Call{Input: 10, Wrote: 40}),
		doneEv("diatom task done 0001"),
		callEv("m2", session.Call{Input: 10, Wrote: 360}),
		doneEv("diatom task ask 0002 Which?"),
	}
	// 400 characters were 200 output tokens: m1 wrote 20, m2 180.
	got := shares([]string{"0001", "0002"}, events, 200)
	w1, w2 := 10.0+5*20, 10.0+5*180
	if fmt.Sprintf("%.4f", got["0001"]) != fmt.Sprintf("%.4f", w1/(w1+w2)) {
		t.Errorf("shares = %v", got)
	}
	c := Session{USD: 10, Tasks: []string{"0001", "0002"}, Shares: got}
	if s := c.Share("0001") + c.Share("0002"); fmt.Sprintf("%.2f", s) != "10.00" {
		t.Errorf("the shares add up to %.2f of 10", s)
	}
	even := Session{USD: 10, Tasks: []string{"0001", "0002"}}
	if fmt.Sprintf("%.2f %.2f", even.Share("0001"), even.Share("0009")) != "5.00 0.00" {
		t.Errorf("an even share = %v", even.Share("0001"))
	}
}
