package queue

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ErrRunning means another scheduler holds the repo's lock.
var ErrRunning = errors.New("a diatom scheduler is already running in this repo")

// ErrNotRunning means no scheduler holds the repo's lock.
var ErrNotRunning = errors.New("no diatom scheduler is running in this repo")

func (s *Store) lockPath() string { return filepath.Join(s.Root, "run.lock") }

// LockScheduler takes the repo's scheduler lock, so there is only ever one
// scheduler per repo (ADR 0007), and returns its release. The lock is an
// flock, so it goes away with the process however the process ends.
func (s *Store) LockScheduler() (func(), error) {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid, _ := os.ReadFile(s.lockPath())
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

// Scheduler returns the process ID of the scheduler running in the repo.
func (s *Store) Scheduler() (int, error) {
	f, err := os.Open(s.lockPath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, ErrNotRunning
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		return 0, ErrNotRunning // nobody holds it
	}
	b, err := os.ReadFile(s.lockPath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(b)))
	if err != nil {
		return 0, fmt.Errorf("the scheduler lock holds no process ID: %w", err)
	}
	return pid, nil
}
