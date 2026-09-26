package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecAndReport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s1")
	spec := Spec{
		ID:           "s1",
		Goal:         "set",
		Tasks:        []string{"0001", "0002"},
		Gate:         "true",
		GateAttempts: 3,
	}
	if err := Create(dir, spec); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar, dir)
	gotDir, got, err := FromEnv()
	if err != nil || gotDir != dir || got.ID != "s1" || len(got.Tasks) != 2 {
		t.Fatalf("FromEnv = %q, %+v, %v", gotDir, got, err)
	}

	for _, e := range []Entry{
		{Type: EntryNote, Task: "0001", Text: "found it"},
		{Type: EntryDone, Task: "0001"},
		{Type: EntryAsk, Task: "0002", Text: "Which timing wins?"},
	} {
		if err := Append(dir, spec, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := Append(dir, spec, Entry{Type: EntryDone, Task: "0009"}); err == nil ||
		!strings.Contains(err.Error(), "not part of this session") {
		t.Errorf("Append of a foreign task: %v", err)
	}

	r, err := ReadReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Done["0001"] || r.Done["0002"] || len(r.Questions) != 1 || len(r.Notes) != 1 {
		t.Errorf("Report = %+v", r)
	}
}

func TestReadReportEmptyAndBroken(t *testing.T) {
	dir := t.TempDir()
	r, err := ReadReport(dir)
	if err != nil || len(r.Done) != 0 {
		t.Errorf("empty ReadReport = %+v, %v", r, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.jsonl"), []byte("{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(dir); err == nil {
		t.Error("broken report parsed")
	}
}

func TestGateState(t *testing.T) {
	dir := t.TempDir()
	g, err := LoadGate(dir)
	if err != nil || g != (GateState{}) {
		t.Errorf("LoadGate before any run = %+v, %v", g, err)
	}
	want := GateState{Attempts: 2, Output: "FAIL", Exhausted: true}
	if err := SaveGate(dir, want); err != nil {
		t.Fatal(err)
	}
	if g, _ := LoadGate(dir); g != want {
		t.Errorf("LoadGate = %+v", g)
	}
	if err := WriteResult(dir, map[string]int{"turns": 3}); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	if _, _, err := FromEnv(); err == nil {
		t.Error("FromEnv outside a session returned no error")
	}
}
