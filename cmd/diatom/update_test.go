package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
