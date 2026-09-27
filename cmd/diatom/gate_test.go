package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestEnsureGate(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Home: base, XDG: filepath.Join(base, "xdg")}
	repo := filepath.Join(base, "vex")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(repo, "go.mod"),
		[]byte("module vex\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	s := queue.Open(repo)

	var out bytes.Buffer
	err := ensureGate(s, paths, strings.NewReader(""), &out, false)
	if err == nil || !strings.Contains(err.Error(), "vex has no gate") ||
		!strings.Contains(err.Error(), "gates.go") {
		t.Fatalf("unasked = %v", err)
	}
	if err := ensureGate(s, paths, strings.NewReader("\n"), &out, true); err == nil {
		t.Error("an empty answer was taken as a gate")
	}
	out.Reset()
	if err := ensureGate(s, paths, strings.NewReader("mage ci:check\n"), &out, true); err != nil ||
		!bytes.Contains(
			out.Bytes(),
			[]byte("Gate: "),
		) || !bytes.Contains(out.Bytes(), []byte("Saved")) {
		t.Fatalf("asked = %v:\n%s", err, out.String())
	}
	cfg, err := config.Load(repo, paths)
	if err != nil || cfg.Gate != "mage ci:check" {
		t.Fatalf("saved gate = %q, %v", cfg.Gate, err)
	}
	// With a gate, nothing is asked.
	out.Reset()
	if err := ensureGate(
		s,
		paths,
		strings.NewReader(""),
		&out,
		true,
	); err != nil ||
		out.Len() != 0 {
		t.Errorf("with a gate = %v, %q", err, out.String())
	}
}
