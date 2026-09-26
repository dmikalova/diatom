package intake

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadDone(t *testing.T) {
	repo := t.TempDir()
	if Dir(repo, "") != filepath.Join(repo, ".diatom", "intake") ||
		Dir(repo, "set") != filepath.Join(repo, ".diatom", "goals", "set", "intake") {
		t.Fatal("Dir is wrong")
	}
	dir := Dir(repo, "set")
	for i, text := range []string{"second\n", "  first  "} {
		if _, err := Write(
			dir,
			Intake{Source: "pane", Created: time.Unix(int64(100-i), 0), Text: text},
		); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := Pending(dir)
	if err != nil || len(pending) != 2 || pending[0].Text != "first" ||
		pending[1].Text != "second" {
		t.Fatalf("Pending = %+v, %v, want oldest first", pending, err)
	}
	if err := Done(pending[0]); err != nil {
		t.Fatal(err)
	}
	if left, _ := Pending(dir); len(left) != 1 {
		t.Errorf("after Done, pending = %+v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "done", filepath.Base(pending[0].Path))); err != nil {
		t.Errorf("Done did not move the intake: %v", err)
	}
	if none, err := Pending(filepath.Join(repo, "missing")); err != nil || none != nil {
		t.Errorf("Pending of a missing dir = %v, %v", none, err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "bad.md"),
		[]byte("no frontmatter"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Pending(dir); err == nil {
		t.Error("a malformed intake was accepted")
	}
}
