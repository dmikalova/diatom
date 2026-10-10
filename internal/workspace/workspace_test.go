package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/queue"
)

// repo makes a git repository at dir with an origin to name it by, and with
// the machine's own excludes out of the way. The state goes to a directory
// of the test's own, never the real one.
func repo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "core.excludesFile", os.DevNull},
		{"remote", "add", "origin", "git@github.com:org/" + filepath.Base(dir) + ".git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// tempDir is t.TempDir with the symlinks git resolves already resolved, and
// points diatom's state at a directory of the test's own.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIATOM_STATE", filepath.Join(dir, "state"))
	return dir
}

func TestOpenInsideARepo(t *testing.T) {
	root := repo(t, filepath.Join(tempDir(t), "engine"))
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := Open(context.Background(), deep)
	if err != nil {
		t.Fatal(err)
	}
	if w.Root != root || len(w.Repos) != 1 || w.One() == nil || w.One().Repo() != root {
		t.Errorf("workspace = %+v", w)
	}
}

func TestOpenOverADirectoryOfRepos(t *testing.T) {
	dir := tempDir(t)
	repo(t, filepath.Join(dir, "api"))
	repo(t, filepath.Join(dir, "web"))
	// Neither of these is a repo, so neither is in the workspace.
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo(t, filepath.Join(dir, "deep", "nested"))

	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Root != dir || len(w.Repos) != 2 || w.One() != nil {
		t.Fatalf("workspace = %+v", w)
	}
	if Name(w.Repos[0]) != "api" || Name(w.Repos[1]) != "web" {
		t.Errorf("repos = %s, %s", Name(w.Repos[0]), Name(w.Repos[1]))
	}
	if w.Store(filepath.Join(dir, "web")) == nil || w.Store(filepath.Join(dir, "notes")) != nil {
		t.Error("Store doesn't match the workspace's repos")
	}
}

// TestOpenLeavesOutARepoWithNoOrigin pins that a repo diatom cannot name is
// left out, and the rest of the workspace still opens (ADR 0013).
func TestOpenLeavesOutARepoWithNoOrigin(t *testing.T) {
	dir := tempDir(t)
	nameless := repo(t, filepath.Join(dir, "api"))
	repo(t, filepath.Join(dir, "web"))
	cmd := exec.Command("git", "remote", "remove", "origin")
	cmd.Dir = nameless
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote remove: %v\n%s", err, out)
	}
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Repos) != 1 || Name(w.Repos[0]) != "web" {
		t.Errorf("repos = %+v, want web alone", w.Repos)
	}
	skip, ok := w.Skipped[nameless]
	if !ok || !strings.Contains(skip.Why, "origin") || skip.Kind != queue.ProblemRepo {
		t.Errorf("skipped = %v, want api said to have no origin", w.Skipped)
	}
}

// TestOpenRefusesASecondCheckout pins that two checkouts of one remote would
// claim one state directory, so the second is left out (ADR 0013).
func TestOpenRefusesASecondCheckout(t *testing.T) {
	dir := tempDir(t)
	repo(t, filepath.Join(dir, "api"))
	twin := filepath.Join(dir, "twin")
	repo(t, twin)
	cmd := exec.Command("git", "remote", "set-url", "origin", "git@github.com:org/api.git")
	cmd.Dir = twin
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url: %v\n%s", err, out)
	}
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Repos) != 1 || len(w.Skipped) != 1 {
		t.Errorf("repos = %+v, skipped = %v", w.Repos, w.Skipped)
	}
	for _, skip := range w.Skipped {
		if skip.Kind != queue.ProblemCheckout {
			t.Errorf(
				"the second checkout is a %s problem, want %s",
				skip.Kind,
				queue.ProblemCheckout,
			)
		}
	}
}

func TestOpenOutsideEverything(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Error("a directory with no repo in it opened")
	}
}
