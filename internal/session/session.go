// Package session is the directory one agent session works from. The harness
// writes the session's spec before starting the agent. The agent's hooks and
// its task tool, which are both `diatom` subcommands running inside the
// session, find the directory through the DIATOM_SESSION environment variable
// and write their results next to the spec. The harness reads them once the
// session ends. This keeps every file the agent influences out of the queue
// directories, which only the harness moves (ADR 0002).
//
//	sessions/<id>/
//	  spec.json      what the session is for, written by the harness
//	  prompt.md      the prompt the agent was given
//	  report.jsonl   the task tool's done, ask and note entries
//	  gate.json      the Stop hook's gate attempts
//	  events.jsonl   the agent's progress events, for the status pane
//	  result.json    how the session ended and what it spent
package session

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

	"github.com/dmikalova/diatom/internal/queue"
)

// EnvVar names the session directory in the agent's environment.
const EnvVar = "DIATOM_SESSION"

// Spec is what a session is for.
type Spec struct {
	ID         string     `json:"id"`
	Repo       string     `json:"repo"`
	Goal       string     `json:"goal"`
	Workstream string     `json:"workstream"`
	Worktree   string     `json:"worktree"`
	Kind       queue.Kind `json:"kind"`
	Profile    string     `json:"profile"`
	Tasks      []string   `json:"tasks"`
	// Gate is the command the Stop hook runs, and GateAttempts how many
	// failures it sends back to the agent before letting the session end.
	Gate         string `json:"gate"`
	GateAttempts int    `json:"gateAttempts"`
}

// Create makes the session directory and writes its spec.
func Create(dir string, s Spec) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "spec.json"), s)
}

// Load reads a session's spec.
func Load(dir string) (Spec, error) {
	var s Spec
	err := readJSON(filepath.Join(dir, "spec.json"), &s)
	return s, err
}

// FromEnv reads the spec of the session the current process runs in.
func FromEnv() (dir string, s Spec, err error) {
	dir = os.Getenv(EnvVar)
	if dir == "" {
		return "", Spec{}, fmt.Errorf(
			"%s is not set: this command only runs inside a diatom agent session",
			EnvVar,
		)
	}
	s, err = Load(dir)
	return dir, s, err
}

// Entry is one thing the agent reported through the task tool.
type Entry struct {
	// Type is done, ask or note.
	Type string `json:"type"`
	Task string `json:"task"`
	Text string `json:"text,omitempty"`
	// Tree is the worktree's files when a revision was marked done, which
	// splits a session's work into one fixup per revision.
	Tree string `json:"tree,omitempty"`
	// Title, Workstream, After and Profile describe a task triage adds, or
	// with Title alone a goal it starts.
	Title      string   `json:"title,omitempty"`
	Workstream string   `json:"workstream,omitempty"`
	After      []string `json:"after,omitempty"`
	Profile    string   `json:"profile,omitempty"`
}

// The entry types.
const (
	EntryDone = "done"
	EntryAsk  = "ask"
	EntryNote = "note"
	// EntryAdd is a task triage adds to one of the goal's workstreams.
	EntryAdd = "add"
	// EntryGoal is a new goal triage starts from part of an intake.
	EntryGoal = "goal"
	// EntryPlan is the plan grilling hands in, as YAML in Text.
	EntryPlan = "plan"
)

// Append adds an entry to the session's report, after checking that it names
// one of the session's tasks.
func Append(dir string, s Spec, e Entry) error {
	if !slices.Contains(s.Tasks, e.Task) {
		return fmt.Errorf(
			"task %s is not part of this session; its tasks are %s",
			e.Task,
			strings.Join(s.Tasks, ", "),
		)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(
		filepath.Join(dir, "report.jsonl"),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Report is what the agent reported over the whole session.
type Report struct {
	// Done are the tasks the agent finished.
	Done map[string]bool
	// Finished are the done entries in the order the agent reported them.
	Finished []Entry
	// Questions are the questions the agent asked, in order.
	Questions []Entry
	// Notes are the notes the agent added, in order.
	Notes []Entry
	// Adds, Goals and Plans are what triage and grilling handed in, in order.
	Adds, Goals, Plans []Entry
}

// ReadReport reads the session's report.
func ReadReport(dir string) (Report, error) {
	r := Report{Done: map[string]bool{}}
	f, err := os.Open(filepath.Join(dir, "report.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return r, fmt.Errorf("report.jsonl: %w", err)
		}
		switch e.Type {
		case EntryDone:
			r.Done[e.Task] = true
			r.Finished = append(r.Finished, e)
		case EntryAsk:
			r.Questions = append(r.Questions, e)
		case EntryNote:
			r.Notes = append(r.Notes, e)
		case EntryAdd:
			r.Adds = append(r.Adds, e)
		case EntryGoal:
			r.Goals = append(r.Goals, e)
		case EntryPlan:
			r.Plans = append(r.Plans, e)
		}
	}
	return r, sc.Err()
}

// GateState is the Stop hook's record of the gate across one session.
type GateState struct {
	// Attempts counts the failed runs.
	Attempts int `json:"attempts"`
	// Passed is the fingerprint of the files the gate last passed on, so the
	// harness need not run it again on the same files.
	Passed string `json:"passed,omitempty"`
	// Output is the last failure's output.
	Output string `json:"output,omitempty"`
	// Exhausted is set once the failures reach the session's limit and the
	// hook let the session end with the gate failing.
	Exhausted bool `json:"exhausted,omitempty"`
}

// LoadGate reads the session's gate state; a session that never ran the gate
// has the zero state.
func LoadGate(dir string) (GateState, error) {
	var g GateState
	err := readJSON(filepath.Join(dir, "gate.json"), &g)
	if errors.Is(err, fs.ErrNotExist) {
		return GateState{}, nil
	}
	return g, err
}

// SaveGate writes the session's gate state.
func SaveGate(dir string, g GateState) error {
	return writeJSON(filepath.Join(dir, "gate.json"), g)
}

// WriteResult records how the session ended, for the status pane.
func WriteResult(dir string, v any) error {
	return writeJSON(filepath.Join(dir, "result.json"), v)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeJSON replaces path atomically, so a reader never sees half a file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
