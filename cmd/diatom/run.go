package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/harness"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner/claude"
	"github.com/dmikalova/diatom/internal/update"
)

// repeatGrace is how soon after a stop another one counts as the same stop
// sent twice, rather than a second asking to exit at once.
const repeatGrace = 2 * time.Second

// updateEvery is how often a running scheduler looks for a new release.
const updateEvery = time.Hour

// updateTimeout bounds looking for a release and installing it.
const updateTimeout = 5 * time.Minute

// cmdRun runs the scheduler. An interrupt or SIGTERM suspends the running
// sessions, which resume where they stopped the next time the scheduler
// starts; a second one, a moment later, exits at once. SIGUSR1 drains instead: no new
// sessions start, and the scheduler exits once the running ones finish.
//
// With autoUpdate set, a new release is installed at startup and then hourly,
// and the scheduler hands itself over to it, suspending its sessions first.
func cmdRun(ctx context.Context, stderr io.Writer) error {
	s, err := here(ctx)
	if err != nil {
		return err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if err := ensureGate(s, paths, os.Stdin, stderr, terminal(os.Stdin)); err != nil {
		return err
	}
	home, err := config.LoadHome(paths)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	up := updater{log: log, auto: home.AutoUpdate, current: update.Current(), goCmd: update.RunGo}
	if bin := up.check(context.Background()); bin != "" {
		// Nothing runs yet, so the new release takes over straight away.
		return update.Exec(bin)
	}

	unlock, err := s.LockScheduler()
	if err != nil {
		return err
	}
	defer unlock()
	exe, err := self()
	if err != nil {
		return err
	}

	drain, drained := context.WithCancel(context.Background())
	defer drained()
	suspend, suspended := context.WithCancel(context.Background())
	defer suspended()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(sigs)
	go func() {
		var since time.Time
		for sig := range sigs {
			switch {
			case sig == syscall.SIGUSR1:
				log.Info(
					"draining: no new sessions start, and diatom exits once the running ones finish",
				)
				drained()
			case suspend.Err() == nil:
				log.Info(
					"suspending: sessions stop within seconds and resume when diatom starts again",
				)
				since = time.Now()
				drained()
				suspended()
			case time.Since(since) < repeatGrace:
				// The same stop sent twice, not a second one.
			default:
				log.Warn("stopping now; git work under way may be left half done")
				os.Exit(1)
			}
		}
	}()
	// The scheduler restarts on a new release, or on a new build installed
	// over its binary; either way its sessions suspend and then resume.
	var next string
	var once sync.Once
	restart := func(bin, why string) {
		once.Do(func() {
			log.Info(
				"suspending the sessions to restart on the new diatom",
				"why",
				why,
				"binary",
				bin,
			)
			next = bin
			drained()
			suspended()
		})
	}
	go up.watch(suspend, func(bin string) { restart(bin, "a new release is installed") })
	go watchBinary(suspend, exe, func() { restart(exe, "the diatom binary was replaced") })

	log.Info("diatom scheduler starting", "version", update.Version(), "repo", s.Repo(),
		"autoUpdate", home.AutoUpdate)
	h := &harness.Harness{
		Paths:  paths,
		Root:   s.Repo(),
		Runner: claude.Runner{},
		Exe:    exe,
		Log:    log,
	}
	if err := h.Run(drain, suspend); err != nil || next == "" {
		return err
	}
	unlock()
	return update.Exec(next)
}

// updater looks for new releases of diatom.
type updater struct {
	log *slog.Logger
	// auto installs new releases; without it they are only logged.
	auto bool
	// current is the running release; empty for a build from a checkout,
	// which never updates.
	current string
	goCmd   update.Go
	// told is the newest release already logged as available.
	told string
}

// check looks for a newer release, and with auto set installs it and
// returns the new binary to run.
func (u *updater) check(ctx context.Context) string {
	if u.current == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	latest, err := update.Latest(ctx, u.goCmd)
	if err != nil {
		u.log.Warn("looking for a new diatom release failed", "err", err)
		return ""
	}
	if !update.Newer(latest, u.current) {
		return ""
	}
	if !u.auto {
		if u.told != latest {
			u.told = latest
			u.log.Info("a new diatom release is out: set autoUpdate: true in "+
				"~/.config/diatom/config.toml, or install it with `go install "+update.Package+"@"+latest+"`",
				"running", u.current, "latest", latest)
		}
		return ""
	}
	u.log.Info("installing the new diatom release", "running", u.current, "latest", latest)
	bin, err := update.Install(ctx, u.goCmd, latest)
	if err != nil {
		u.log.Warn("installing the new diatom release failed", "err", err)
		return ""
	}
	return bin
}

// watch checks for a release every updateEvery until ctx is done, and calls
// restart with the first one installed.
func (u *updater) watch(ctx context.Context, restart func(bin string)) {
	if u.current == "" {
		return
	}
	tick := time.NewTicker(updateEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if bin := u.check(ctx); bin != "" {
				restart(bin)
				return
			}
		}
	}
}

// cmdStop asks the running scheduler to suspend its sessions and exit, or
// with -drain to start no new ones and exit once the running ones finish.
func cmdStop(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	drain := fs.Bool("drain", false, "let the running sessions finish instead of suspending them")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fmt.Errorf("%w: stop takes only -drain", errUsage)
	}
	s, err := here(ctx)
	if err != nil {
		return err
	}
	pid, err := s.Scheduler()
	if errors.Is(err, queue.ErrNotRunning) {
		_, _ = fmt.Fprintln(stdout, "no diatom scheduler is running in this repo")
		return nil
	}
	if err != nil {
		return err
	}
	sig, what := syscall.SIGTERM, "suspend its sessions and exit; they resume on the next `diatom run`"
	if *drain {
		sig, what = syscall.SIGUSR1, "start no new sessions and exit once the running ones finish"
	}
	if err := syscall.Kill(pid, sig); err != nil {
		return fmt.Errorf("signalling the scheduler (pid %d): %w", pid, err)
	}
	_, _ = fmt.Fprintf(stdout, "asked the scheduler (pid %d) to %s\n", pid, what)
	return nil
}
