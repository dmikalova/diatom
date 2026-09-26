package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/queue"
)

// fakeGo stands in for the go command, with latest as the newest release.
func fakeGo(
	t *testing.T,
	latest string,
	installs *int,
) func(context.Context, string, ...string) ([]byte, error) {
	gobin := t.TempDir()
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "list":
			return []byte(`{"Version":"` + latest + `"}`), nil
		case "install":
			*installs++
			return nil, os.WriteFile(filepath.Join(gobin, "diatom"), nil, 0o755)
		}
		return []byte(gobin + "\n"), nil
	}
}

func TestUpdaterCheck(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	installs := 0
	ctx := context.Background()

	off := updater{log: log, current: "v0.3.0", goCmd: fakeGo(t, "v0.4.0", &installs)}
	if bin := off.check(
		ctx,
	); bin != "" || installs != 0 ||
		!bytes.Contains(logs.Bytes(), []byte("set autoUpdate: true")) {
		t.Errorf("without autoUpdate: bin %q, %d installs, logs %s", bin, installs, logs.String())
	}
	logs.Reset()
	off.check(ctx)
	if logs.Len() != 0 {
		t.Error("the same release was announced twice")
	}

	on := updater{log: log, auto: true, current: "v0.3.0", goCmd: fakeGo(t, "v0.4.0", &installs)}
	if bin := on.check(ctx); !strings.HasSuffix(bin, "/diatom") || installs != 1 {
		t.Errorf("with autoUpdate: bin %q, %d installs", bin, installs)
	}
	upToDate := updater{
		log:     log,
		auto:    true,
		current: "v0.4.0",
		goCmd:   fakeGo(t, "v0.4.0", &installs),
	}
	if bin := upToDate.check(ctx); bin != "" || installs != 1 {
		t.Error("installed the release already running")
	}
	checkout := updater{log: log, auto: true, current: "", goCmd: fakeGo(t, "v9.0.0", &installs)}
	if bin := checkout.check(ctx); bin != "" || installs != 1 {
		t.Error("a build from a checkout updated itself")
	}
}

func TestStop(t *testing.T) {
	repo := inRepo(t)
	if code, stdout, _ := diatom(
		t,
		"",
		"stop",
	); code != 0 ||
		!strings.Contains(stdout, "no diatom scheduler") {
		t.Errorf("stop with nothing running = %d %q", code, stdout)
	}
	// This test process stands in for the repo's scheduler.
	unlock, err := queue.Open(repo).LockScheduler()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1)
	defer signal.Stop(sigs)
	if code, stdout, _ := diatom(
		t,
		"",
		"stop",
		"-drain",
	); code != 0 ||
		!strings.Contains(stdout, "once the running ones finish") {
		t.Errorf("stop -drain = %d %q", code, stdout)
	}
	select {
	case sig := <-sigs:
		if sig != syscall.SIGUSR1 {
			t.Errorf("got %v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Error("the scheduler was not signalled")
	}
	if code, _, _ := diatom(t, "", "stop", "now"); code != 2 {
		t.Error("stop took an argument")
	}
}

// pane stands in for a workspace pane.
type pane struct{ editing bool }

func (p *pane) Init() tea.Cmd                       { return nil }
func (p *pane) Update(tea.Msg) (tea.Model, tea.Cmd) { return p, nil }
func (p *pane) View() tea.View                      { return tea.NewView("") }
func (p *pane) Editing() bool                       { return p.editing }

func TestRestartWaitsForEditing(t *testing.T) {
	inner := &pane{editing: true}
	r := &restartable{inner: inner}
	if _, cmd := r.Update(binaryChanged{}); cmd != nil || r.restart {
		t.Fatal("restarted in the middle of an answer")
	}
	inner.editing = false
	if _, cmd := r.Update(tea.KeyPressMsg{}); cmd == nil || !r.restart {
		t.Error("did not restart once the answer was sent")
	}
}

func TestWatchBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diatom")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{})
	go watchBinary(context.Background(), path, func() { close(changed) })
	time.Sleep(100 * time.Millisecond)
	// go install writes the new binary beside the old and renames it over.
	if err := os.WriteFile(path+".new", []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(3 * binaryPoll):
		t.Error("a replaced binary went unnoticed")
	}
}
