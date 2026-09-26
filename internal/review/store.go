package review

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dmikalova/diatom/internal/intake"
)

// Decision is the human's verdict on one hunk of one commit (ADR 0001).
type Decision string

// The decisions.
const (
	Approve Decision = "approve"
	Reject  Decision = "reject"
	// Defer means "not now": the hunk stays in the queue behind unreviewed
	// hunks and creates no agent work.
	Defer Decision = "defer"
)

// Comment is a note on one line of a hunk.
type Comment struct {
	// Line is the index of the line in the hunk's fragment.
	Line int    `yaml:"line"`
	Text string `yaml:"text"`
}

// Record is the decision on one hunk, fixed to its commit: a later commit
// that rewrites the same code never reopens it.
type Record struct {
	Decision Decision  `yaml:"decision"`
	Comments []Comment `yaml:"comments,omitempty"`
	At       time.Time `yaml:"at"`
	// Seq counts the changes to the record. A revision records the Seq of
	// each rejection it carries, so a changed rejection is noticed.
	Seq int `yaml:"seq"`
}

// Store holds the review records of one goal:
//
//	reviews/<sha>.yaml     the decisions on one commit's hunks
//	reviews/history.jsonl  every decision in order, for stepping back
//	intake/<time>.md       comments on approved hunks (ADR 0009)
type Store struct {
	// Dir is the goal's directory.
	Dir string
}

// Commit is the review of one commit: its records keyed by hunk ID.
type Commit struct {
	Hunks map[string]*Record `yaml:"hunks"`
}

func (s Store) path(sha string) string { return filepath.Join(s.Dir, "reviews", sha+".yaml") }

// Load reads a commit's review; a commit nobody reviewed has no records.
func (s Store) Load(sha string) (*Commit, error) {
	c := &Commit{Hunks: map[string]*Record{}}
	b, err := os.ReadFile(s.path(sha))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("review of %s: %w", short(sha), err)
	}
	if c.Hunks == nil {
		c.Hunks = map[string]*Record{}
	}
	return c, nil
}

// Decide records a decision on a hunk and appends it to the history. A
// comment on an approved hunk also becomes an intake (ADR 0001).
func (s Store) Decide(h Hunk, d Decision, comments []Comment, now time.Time) (*Record, error) {
	c, err := s.Load(h.Commit)
	if err != nil {
		return nil, err
	}
	rec := c.Hunks[h.ID]
	seq := 1
	if rec != nil {
		seq = rec.Seq + 1
	}
	rec = &Record{Decision: d, Comments: comments, At: now, Seq: seq}
	c.Hunks[h.ID] = rec
	b, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(s.path(h.Commit), b); err != nil {
		return nil, err
	}
	if err := s.appendHistory(
		Event{Commit: h.Commit, Hunk: h.ID, Decision: d, At: now},
	); err != nil {
		return nil, err
	}
	if d == Approve && len(comments) > 0 {
		if err := s.intake(h, comments, now); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

// Event is one decision in the history.
type Event struct {
	Commit   string    `json:"commit"`
	Hunk     string    `json:"hunk"`
	Decision Decision  `json:"decision"`
	At       time.Time `json:"at"`
}

func (s Store) appendHistory(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "reviews", "history.jsonl"),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// History returns every decision, oldest first.
func (s Store) History() ([]Event, error) {
	f, err := os.Open(filepath.Join(s.Dir, "reviews", "history.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var events []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("history.jsonl: %w", err)
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// intake writes a comment on an approved hunk as intake for triage.
func (s Store) intake(h Hunk, comments []Comment, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Comments on an approved hunk of %s:\n\n```diff\n%s\n```\n\n", h.Path, h.Text())
	for _, c := range comments {
		fmt.Fprintf(&b, "- Line %d: %s\n", h.NewLine(c.Line), c.Text)
	}
	_, err := intake.Write(filepath.Join(s.Dir, "intake"), intake.Intake{
		Source: "review", Created: now, Commit: h.Commit, Hunk: h.ID, Text: b.String(),
	})
	return err
}

// writeAtomic replaces path through a temp file in the same directory.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name())
		return werr
	}
	return os.Rename(f.Name(), path)
}
