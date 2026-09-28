package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// EventsFile is where a session's progress is logged, one JSON event a line:
// the agent's words, its tool calls and their results, and the gate's runs.
const EventsFile = "events.jsonl"

// EventGate is a gate run, and EventSettle a step diatom takes after the
// agent, such as committing; the other types are the runner's, EventCall
// among them: one of the agent's model calls, which is no step of its own.
const (
	EventGate   = "gate"
	EventSettle = "settle"
	EventCall   = "call"
)

// Call is what one of the agent's model calls read, in tokens, and wrote, in
// characters, as the runner logs it.
type Call struct {
	Input        int `json:"input"`
	CacheWrite   int `json:"cacheWrite,omitempty"`
	CacheWrite1h int `json:"cacheWrite1h,omitempty"`
	CacheRead    int `json:"cacheRead,omitempty"`
	Wrote        int `json:"wrote,omitempty"`
}

// Settling records a step diatom starts on after the agent.
func Settling(now time.Time, what string) Event {
	return Event{Time: now, Type: EventSettle, Summary: what}
}

// Event is one line of a session's events.
type Event struct {
	Time time.Time `json:"time"`
	Type string    `json:"type"`
	Text string    `json:"text,omitempty"`
	// ID links a tool call to its result.
	ID string `json:"id,omitempty"`
	// Summary says in a few words what the step does.
	Summary string `json:"summary,omitempty"`
	// Detail is a tool call's whole input, or the end of an output.
	Detail string `json:"detail,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	Call   *Call  `json:"call,omitempty"`
}

// GateEvent records a gate run that took took.
func GateEvent(now time.Time, gate string, took time.Duration, passed bool, output string) Event {
	how := "passed"
	if !passed {
		how = "failed"
	}
	return Event{
		Time: now, Type: EventGate, Detail: output, Failed: !passed,
		Summary: fmt.Sprintf("Gate `%s` %s in %s", gate, how, took.Round(100*time.Millisecond)),
	}
}

// AppendEvent adds an event to a session's log.
func AppendEvent(dir string, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(
		filepath.Join(dir, EventsFile),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// ReadEvents reads a session's log; a line that isn't an event is skipped.
func ReadEvents(dir string) ([]Event, error) {
	f, err := os.Open(filepath.Join(dir, EventsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	return events, sc.Err()
}
