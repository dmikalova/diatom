package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/git"
)

// TestMigrateMovesTheStateOutAndRepairsTheWorktrees pins the one-time move
// of a repo diatom used before ADR 0013: its `.diatom/` leaves the repo, and
// git is told where the worktrees in it went, so they still work.
func TestMigrateMovesTheStateOutAndRepairsTheWorktrees(t *testing.T) {
	ctx := context.Background()
	dir := repo(t, "git@github.com:me/toy.git")
	r := git.Repo{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "docs: readme"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	wt := filepath.Join(dir, ".diatom", "goals", "set", "worktrees", "engine")
	if _, err := r.Run(ctx, "worktree", "add", "-b", "diatom/set/engine", wt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".diatom", "goals", "set", "goal.yaml"),
		[]byte("name: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	moved, err := Migrate(ctx, base, dir)
	if err != nil || !moved {
		t.Fatalf("Migrate = %v, %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".diatom")); !os.IsNotExist(err) {
		t.Error("the repo still holds a .diatom directory")
	}
	root := filepath.Join(base, "github.com", "me", "toy")
	if _, err := os.Stat(filepath.Join(root, "goals", "set", "goal.yaml")); err != nil {
		t.Fatalf("the goal did not come across: %v", err)
	}
	out, err := (git.Repo{Dir: filepath.Join(root, "goals", "set", "worktrees", "engine")}).
		Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || strings.TrimSpace(out) != "diatom/set/engine" {
		t.Errorf("the worktree was not repaired: %q, %v", out, err)
	}
	if again, err := Migrate(ctx, base, dir); err != nil || again {
		t.Errorf("migrating again = %v, %v, want nothing left to do", again, err)
	}
}

// TestUnignoreTakesDiatomOutOfTheGitignore pins that the line the old layout
// needed goes once nothing of diatom is in the repo.
func TestUnignoreTakesDiatomOutOfTheGitignore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\n.diatom/\ndist\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unignore(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); strings.Contains(got, ".diatom") ||
		!strings.Contains(got, "node_modules/") || !strings.Contains(got, "dist") {
		t.Errorf("gitignore = %q", got)
	}
}
