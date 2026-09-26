package registry

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestRegistry(t *testing.T) {
	base := t.TempDir()
	r := Registry{Path: filepath.Join(base, "state", "repos")}
	if repos, err := r.List(); err != nil || repos != nil {
		t.Errorf("List of a missing registry = %v, %v", repos, err)
	}
	a, b := t.TempDir(), t.TempDir()
	for _, repo := range []string{a, b, a} {
		if err := r.Add(repo); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Add(filepath.Join(base, "gone")); err != nil {
		t.Fatal(err)
	}
	repos, err := r.List()
	if err != nil || !slices.Equal(repos, []string{a, b}) {
		t.Errorf("List = %v, %v, want each existing repo once", repos, err)
	}
}

func TestDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	r, err := Default()
	if err != nil || r.Path != "/state/diatom/repos" {
		t.Errorf("Default = %+v, %v", r, err)
	}
}

func TestLockScheduler(t *testing.T) {
	r := Registry{Path: filepath.Join(t.TempDir(), "diatom", "repos")}
	unlock, err := r.LockScheduler()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.LockScheduler(); !errors.Is(err, ErrRunning) {
		t.Errorf("second lock = %v, want ErrRunning", err)
	}
	unlock()
	again, err := r.LockScheduler()
	if err != nil {
		t.Fatalf("lock after release = %v", err)
	}
	again()
}
