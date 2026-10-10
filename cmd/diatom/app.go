package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/harness"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner/claude"
	"github.com/dmikalova/diatom/internal/ui"
	"github.com/dmikalova/diatom/internal/update"
	"github.com/dmikalova/diatom/internal/workspace"
)

// logKeep is how big the scheduler's log may grow before the next start sets
// it aside.
const logKeep = 10 << 20

// errStoppedAtOnce is a quit that didn't wait for the sessions to suspend.
var errStoppedAtOnce = errors.New("stopped at once; git work under way may be left half done")

// cmdApp opens diatom on the workspace: one window over the repo it is in,
// or over every repo under the directory it is in, which runs the scheduler
// too (ADR 0007). Quitting, or closing the terminal, suspends the running
// sessions, which carry on the next time diatom opens; nothing runs while it
// is closed. With diatom open on a repo elsewhere, this one only views.
func cmdApp(ctx context.Context) error {
	w, err := hereWorkspace(ctx)
	if err != nil {
		return err
	}
	s := w.Repos[0]
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if err := config.Ensure(paths); err != nil {
		return err
	}
	// The config is read once: a change to it takes effect when diatom
	// next starts, as U does, and a mistake in it can't stop the running
	// scheduler.
	cfg, err := config.Load(s.Repo(), paths.For(s.Key()))
	if err != nil {
		return err
	}
	logFile, err := openLog(s)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewTextHandler(logFile, nil))
	up := updater{log: log, auto: cfg.AutoUpdate, current: update.Current(), goCmd: update.RunGo}
	if bin := up.check(ctx); bin != "" {
		// Nothing runs yet, so the new release takes over straight away.
		return update.Exec(bin)
	}

	sched, err := startScheduler(w, paths, cfg, log)
	if err != nil {
		return err
	}
	defer sched.unlock()
	env := ui.Env{
		Store: s, Repos: w.Repos, Root: w.Root,
		Paths: paths, Now: time.Now, Config: cfg,
	}
	app := ui.NewApp(ctx, env, sched.Scheduler)
	p := tea.NewProgram(app, tea.WithoutSignalHandler())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		for range sigs {
			p.Send(ui.QuitMsg{})
		}
	}()
	// A newer diatom waits for the human to restart on it, rather than
	// restarting the window under them.
	watch, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	if exe, err := self(); err == nil {
		go up.watch(watch, func(bin string) {
			p.Send(ui.UpdateMsg{Bin: bin, Why: "a new release is installed"})
		})
		go watchBinary(watch, exe, func() {
			p.Send(ui.UpdateMsg{Bin: exe, Why: "a new build is installed"})
		})
	}
	_, err = p.Run()
	if app.StoppedAtOnce() {
		return errStoppedAtOnce
	}
	// However the window closed, the sessions are suspended before diatom
	// exits.
	if sched.Stop != nil {
		sched.Stop()
		<-sched.Done
	}
	if err := errors.Join(err, sched.err); err != nil || app.Restart() == "" {
		return err
	}
	sched.unlock()
	return update.Exec(app.Restart())
}

// scheduler is the scheduler cmdApp runs, and err why it stopped, once Done
// is closed.
type scheduler struct {
	ui.Scheduler
	err    error
	unlock func()
}

// startScheduler runs the workspace's scheduler in the background, or when
// diatom is open on one of its repos elsewhere, none: the app then only
// views. One scheduler covers every repo, so its session limit is shared.
func startScheduler(
	w *workspace.Workspace,
	paths config.Paths,
	cfg *config.Config,
	log *slog.Logger,
) (*scheduler, error) {
	repos := w.Repos
	var unlocks []func()
	unlock := func() {
		for _, u := range unlocks {
			u()
		}
		unlocks = nil
	}
	roots := make([]string, 0, len(repos))
	for _, s := range repos {
		u, err := s.LockScheduler()
		if err != nil {
			unlock()
			pid, perr := s.Scheduler()
			if perr != nil {
				return nil, err
			}
			return &scheduler{
				Viewer: fmt.Sprintf("diatom is open on %s in pid %d", filepath.Base(s.Repo()), pid),
				unlock: func() {},
			}, nil
		}
		unlocks = append(unlocks, u)
		roots = append(roots, s.Repo())
	}
	exe, err := self()
	if err != nil {
		unlock()
		return nil, err
	}
	drain, drained := context.WithCancel(context.Background())
	suspend, suspended := context.WithCancel(context.Background())
	done := make(chan struct{})
	sc := &scheduler{unlock: unlock}
	sc.Done = done
	sc.Stop = func() {
		if suspend.Err() == nil {
			log.Info("suspending: sessions stop within seconds and resume when diatom opens again")
		}
		drained()
		suspended()
	}
	log.Info("diatom scheduler starting", "version", update.Version(), "repos", roots)
	h := &harness.Harness{
		Paths: paths, Roots: roots, Stores: repos, Skipped: w.Skipped,
		Config: cfg, Runner: claude.Runner{}, Exe: exe, Log: log,
	}
	go func() {
		defer close(done)
		if sc.err = h.Run(drain, suspend); sc.err != nil {
			log.Error("the scheduler stopped", "err", sc.err)
		}
	}()
	return sc, nil
}

// openLog opens the scheduler's log for appending, setting a big one aside
// first.
func openLog(s *queue.Store) (io.WriteCloser, error) {
	path := ui.LogPath(s)
	if fi, err := os.Stat(path); err == nil && fi.Size() > logKeep {
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
