// Package focus is the goal the human is looking at (ADR 0007). The status
// pane sets it, and the review and intake panes follow it, so switching goals
// is one keypress rather than one per pane.
package focus

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Focus names a repo and, once one is picked, a goal in it.
type Focus struct {
	Repo string `yaml:"repo"`
	Goal string `yaml:"goal,omitempty"`
}

// File is where the focus is kept.
type File struct {
	Path string
}

// Default returns the focus file under XDG_STATE_HOME.
func Default() (File, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return File{}, err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return File{Path: filepath.Join(state, "diatom", "focus.yaml")}, nil
}

// Read returns the focus, which is empty until one is set.
func (f File) Read() (Focus, error) {
	var fc Focus
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return fc, nil
	}
	if err != nil {
		return fc, err
	}
	err = yaml.Unmarshal(b, &fc)
	return fc, err
}

// Write sets the focus.
func (f File) Write(fc Focus) error {
	b, err := yaml.Marshal(fc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}
