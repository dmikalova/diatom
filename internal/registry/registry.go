// Package registry lists the repos the scheduler knows about. One headless
// scheduler per machine picks work across every known repo (ADR 0007); a repo
// becomes known when a goal is created in it.
package registry

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Registry is the file that lists the known repos, one path per line.
type Registry struct {
	Path string
}

// Default returns the registry under XDG_STATE_HOME.
func Default() (Registry, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Registry{}, err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return Registry{Path: filepath.Join(state, "diatom", "repos")}, nil
}

// List returns the known repos that still exist.
func (r Registry) List() ([]string, error) {
	b, err := os.ReadFile(r.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var repos []string
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || slices.Contains(repos, line) {
			continue
		}
		if _, err := os.Stat(line); err == nil {
			repos = append(repos, line)
		}
	}
	return repos, nil
}

// Add records repo as known.
func (r Registry) Add(repo string) error {
	repos, err := r.List()
	if err != nil {
		return err
	}
	if slices.Contains(repos, repo) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	tmp := r.Path + ".tmp"
	if err := os.WriteFile(
		tmp,
		[]byte(strings.Join(append(repos, repo), "\n")+"\n"),
		0o644,
	); err != nil {
		return err
	}
	return os.Rename(tmp, r.Path)
}
