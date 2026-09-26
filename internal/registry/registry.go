// Package registry lists the repos the scheduler knows about. One headless
// scheduler per machine picks work across every known repo (ADR 0007); a repo
// becomes known when a goal is created in it.
package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// ErrRunning means another scheduler holds the lock.
var ErrRunning = errors.New("a diatom scheduler is already running on this machine")

// LockScheduler takes the machine's scheduler lock, so there is only ever one
// scheduler (ADR 0007), and returns its release. The lock is an flock, so it
// goes away with the process however the process ends.
func (r Registry) LockScheduler() (func(), error) {
	path := filepath.Join(filepath.Dir(r.Path), "run.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid, _ := os.ReadFile(path)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (pid %s)", ErrRunning, string(bytes.TrimSpace(pid)))
		}
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() { _ = f.Close() }, nil
}
