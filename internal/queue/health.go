package queue

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Stuck is why the scheduler can't plan: an error every pass hits, such as a
// config it can't read, which leaves every goal waiting.
type Stuck struct {
	Error string `json:"error"`
	// Since is when the scheduler first hit it.
	Since time.Time `json:"since"`
}

func (s *Store) stuckPath() string { return filepath.Join(s.Root, "stuck.json") }

// SetStuck records why the scheduler couldn't plan, keeping when it first
// happened while the error stays the same; an empty msg clears it.
func (s *Store) SetStuck(msg string, now time.Time) error {
	if msg == "" {
		err := os.Remove(s.stuckPath())
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if old, err := s.Stuck(); err == nil && old != nil && old.Error == msg {
		return nil
	}
	b, err := json.Marshal(Stuck{Error: msg, Since: now})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.stuckPath(), b, 0o644)
}

// Stuck reads why the scheduler can't plan, or nil when it can.
func (s *Store) Stuck() (*Stuck, error) {
	b, err := os.ReadFile(s.stuckPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st Stuck
	return &st, json.Unmarshal(b, &st)
}
