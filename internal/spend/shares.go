package spend

import (
	"regexp"
	"slices"

	"github.com/dmikalova/diatom/internal/session"
)

// reported finds where the agent reports a task in a command it runs: done,
// asked about, or handed to the human.
var reported = regexp.MustCompile(`\bdiatom task (?:done|ask|manual) (\S+)`)

// The price of each kind of token, relative to a token read fresh, the same
// for every Claude model: cache writes cost a quarter more for five minutes
// and double for an hour, a cache read a tenth, and output five times as
// much.
const (
	cacheWritePrice   = 1.25
	cacheWrite1hPrice = 2
	cacheReadPrice    = 0.1
	outputPrice       = 5
)

// charsPerToken is about how many characters of English and code make a
// token, for telling a call's output from what it wrote.
const charsPerToken = 4

// call is one model call, and the tasks it was made for.
type call struct {
	session.Call
	owners []string
}

// shares tells what part of a session's cost each of its tasks took: each
// model call counts for the task it was made for, the one the agent
// reported next, and what comes after the last report, such as fixing the
// gate, for all of them alike. A call counts by what its tokens cost, the
// output it wrote told from its characters, scaled to output tokens when
// the session's result gives them. It is nil for a session that logged no
// calls.
func shares(tasks []string, events []session.Event, output int) map[string]float64 {
	calls := modelCalls(tasks, events)
	if len(calls) == 0 || len(tasks) == 0 {
		return nil
	}
	wrote := 0
	for _, c := range calls {
		wrote += c.Wrote
	}
	// Tokens per character written, from the session's own output when its
	// result has it.
	perChar := 1.0 / charsPerToken
	if output > 0 && wrote > 0 {
		perChar = float64(output) / float64(wrote)
	}
	weights, total := map[string]float64{}, 0.0
	for _, c := range calls {
		w := float64(c.Input) + cacheWritePrice*float64(c.CacheWrite) +
			cacheWrite1hPrice*float64(c.CacheWrite1h) + cacheReadPrice*float64(c.CacheRead) +
			outputPrice*perChar*float64(c.Wrote)
		owners := c.owners
		if owners == nil {
			owners = tasks
		}
		for _, t := range owners {
			weights[t] += w / float64(len(owners))
		}
		total += w
	}
	if total == 0 {
		return nil
	}
	out := make(map[string]float64, len(tasks))
	for _, t := range tasks {
		out[t] = weights[t] / total
	}
	return out
}

// modelCalls are the session's model calls in order, each once however many
// parts its message came in, with the tasks the agent reported next after
// it; the calls after the last report have none.
func modelCalls(tasks []string, events []session.Event) []*call {
	var calls, pending []*call
	byID := map[string]*call{}
	for _, e := range events {
		switch {
		case e.Type == session.EventCall && e.Call != nil:
			c, ok := byID[e.ID]
			if !ok {
				c = &call{Call: *e.Call}
				c.Wrote = 0
				byID[e.ID] = c
				calls = append(calls, c)
				pending = append(pending, c)
			}
			// Each part of the message counts what it wrote itself.
			c.Wrote += e.Call.Wrote
		case e.Type == "tool":
			if done := reports(tasks, e); len(done) > 0 {
				for _, c := range pending {
					c.owners = done
				}
				pending = nil
			}
		}
	}
	return calls
}

// reports are the session's tasks a tool call reports on, each once.
func reports(tasks []string, e session.Event) []string {
	var done []string
	for _, m := range reported.FindAllStringSubmatch(e.Detail+" "+e.Text, -1) {
		if slices.Contains(tasks, m[1]) && !slices.Contains(done, m[1]) {
			done = append(done, m[1])
		}
	}
	return done
}
