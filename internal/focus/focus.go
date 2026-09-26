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

// Focus names the goal the human is looking at; an empty goal is the repo
// itself.
type Focus struct {
	Goal string `yaml:"goal,omitempty"`
}

// File is where the focus is kept.
type File struct {
	Path string
}

// In returns the focus file of the repo at root.
func In(root string) File {
	return File{Path: filepath.Join(root, ".diatom", "focus.yaml")}
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
