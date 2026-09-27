package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dmikalova/diatom/internal/update"
)

// updateEvery is how often a running scheduler looks for a new release.
const updateEvery = time.Hour

// updateTimeout bounds looking for a release and installing it.
const updateTimeout = 5 * time.Minute

// binaryPoll is how often a running diatom checks whether its binary on disk
// was replaced, by an update or a new build.
const binaryPoll = 5 * time.Second

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
