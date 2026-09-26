package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/update"
)

// binaryPoll is how often a running diatom checks whether its binary on disk
// was replaced, by an update or a new build.
const binaryPoll = 5 * time.Second

// self returns the path of the running binary.
func self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// watchBinary calls changed once, when the file at path is no longer the
// one it was when watching began, until ctx is done.
func watchBinary(ctx context.Context, path string, changed func()) {
	was, err := os.Stat(path)
	if err != nil {
		return
	}
	tick := time.NewTicker(binaryPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now, err := os.Stat(path)
			if err == nil && (!os.SameFile(was, now) || !now.ModTime().Equal(was.ModTime()) ||
				now.Size() != was.Size()) {
				changed()
				return
			}
		}
	}
}

// binaryChanged tells a pane its binary was replaced.
type binaryChanged struct{}

// editor is a pane that may be in the middle of something a restart would
// lose, such as an answer being typed.
type editor interface{ Editing() bool }

// restartable runs a pane until its binary is replaced, then quits it at the
// first moment it isn't editing, so it can start again on the new binary.
type restartable struct {
	inner   tea.Model
	pending bool
	restart bool
}

func (r *restartable) Init() tea.Cmd { return r.inner.Init() }

func (r *restartable) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if _, ok := msg.(binaryChanged); ok {
		r.pending = true
	} else {
		r.inner, cmd = r.inner.Update(msg)
	}
	if e, ok := r.inner.(editor); r.pending && (!ok || !e.Editing()) {
		r.restart = true
		return r, tea.Quit
	}
	return r, cmd
}

func (r *restartable) View() tea.View { return r.inner.View() }

// runProgram runs a pane, and runs it again on the new binary when the
// binary is replaced.
func runProgram(ctx context.Context, m tea.Model) error {
	exe, err := self()
	if err != nil {
		return err
	}
	r := &restartable{inner: m}
	p := tea.NewProgram(r, tea.WithContext(ctx))
	watch, stop := context.WithCancel(ctx)
	defer stop()
	var once sync.Once
	go watchBinary(watch, exe, func() { once.Do(func() { p.Send(binaryChanged{}) }) })
	_, err = p.Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		err = nil
	}
	if err != nil || !r.restart {
		return err
	}
	return update.Exec(exe)
}
