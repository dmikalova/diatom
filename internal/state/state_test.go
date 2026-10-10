package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmikalova/diatom/internal/git"
)

func TestKeyNamesARepoByItsOrigin(t *testing.T) {
	for _, c := range []struct{ remote, want string }{
		{"git@github.com:goodship-io/nextjs.git", "github.com/goodship-io/nextjs"},
		{"https://github.com/goodship-io/nextjs", "github.com/goodship-io/nextjs"},
		{"https://GitHub.com/Goodship-IO/NextJS.git", "github.com/goodship-io/nextjs"},
		{"ssh://git@github.com:22/goodship-io/nextjs.git", "github.com/goodship-io/nextjs"},
		{"git@gitlab.com:group/sub/thing.git", "gitlab.com/group/sub/thing"},
	} {
		got, err := Key(c.remote)
		if err != nil || got != c.want {
			t.Errorf("Key(%q) = %q, %v, want %q", c.remote, got, err, c.want)
		}
	}
	for _, remote := range []string{"", "   ", "/just/a/path"} {
		if got, err := Key(remote); err == nil {
			t.Errorf("Key(%q) = %q, want an error", remote, got)
		}
	}
}

// repo makes a git repo with the given origin.
func repo(t *testing.T, origin string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := git.Repo{Dir: dir}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"remote", "add", "origin", origin},
	} {
		if _, err := r.Run(context.Background(), args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestOpenKeysByOriginAndMarksTheRepo pins that a repo's queue lands under
// its origin's name, with a marker saying which checkout it is of, and that
// a second open of the same repo finds the same directory.
func TestOpenKeysByOriginAndMarksTheRepo(t *testing.T) {
	base := t.TempDir()
	dir := repo(t, "git@github.com:me/toy.git")
	s, err := Open(context.Background(), base, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "github.com", "me", "toy")
	if s.Root != want {
		t.Errorf("root = %s, want %s", s.Root, want)
	}
	if s.Repo() != dir {
		t.Errorf("repo = %s, want %s", s.Repo(), dir)
	}
	b, err := os.ReadFile(filepath.Join(want, MarkerFile))
	if err != nil || string(b) != dir+"\n" {
		t.Errorf("marker = %q, %v", string(b), err)
	}
	again, err := Open(context.Background(), base, dir)
	if err != nil || again.Root != want {
		t.Errorf("opening it again gave %v, %v", again, err)
	}
}

// TestOpenAdoptsARenamedRepo pins that a repo renamed on the forge keeps the
// goals it had: its directory is renamed to the new key, rather than diatom
// starting it over empty.
func TestOpenAdoptsARenamedRepo(t *testing.T) {
	base := t.TempDir()
	dir := repo(t, "git@github.com:me/toy.git")
	ctx := context.Background()
	if _, err := Open(ctx, base, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "github.com", "me", "toy", "mark"),
		[]byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (git.Repo{Dir: dir}).Run(ctx, "remote", "set-url", "origin",
		"git@github.com:me/game.git"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, base, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root != filepath.Join(base, "github.com", "me", "game") {
		t.Fatalf("root = %s", s.Root)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "mark")); err != nil {
		t.Errorf("the goals it had were not carried over: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "github.com", "me", "toy")); !os.IsNotExist(err) {
		t.Error("the old directory is still there")
	}
}

// TestOrphansAreTheReposThatAreGone pins that a queue whose repo is no
// longer where its marker says is found, so diatom can ask about it.
func TestOrphansAreTheReposThatAreGone(t *testing.T) {
	base := t.TempDir()
	dir := repo(t, "git@github.com:me/toy.git")
	if _, err := Open(context.Background(), base, dir); err != nil {
		t.Fatal(err)
	}
	if got := Orphans(base); len(got) != 0 {
		t.Errorf("orphans while the repo is there = %v", got)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	got := Orphans(base)
	if len(got) != 1 || got["github.com/me/toy"] != dir {
		t.Errorf("orphans = %v, want the one repo that is gone", got)
	}
}

// TestRepointAndForgetDealWithAnOrphan pins the two answers to an orphan:
// say where its repo went, or throw away what is kept of it (ADR 0013).
func TestRepointAndForgetDealWithAnOrphan(t *testing.T) {
	base := t.TempDir()
	dir := repo(t, "git@github.com:me/toy.git")
	if _, err := Open(context.Background(), base, dir); err != nil {
		t.Fatal(err)
	}
	const key = "github.com/me/toy"
	moved := repo(t, "git@github.com:me/toy.git")
	if err := Repoint(context.Background(), base, key, moved); err != nil {
		t.Fatal(err)
	}
	if at := Markers(base)[key]; at != moved {
		t.Errorf("the marker says %q, want %q", at, moved)
	}
	if err := Repoint(context.Background(), base, key, filepath.Join(base, "nowhere")); err == nil {
		t.Error("repointing at a path with no repo was allowed")
	}
	if err := Forget(base, ".."); err == nil {
		t.Error("forgetting a directory outside the base was allowed")
	}
	if err := Forget(base, key); err != nil {
		t.Fatal(err)
	}
	if got := Markers(base); len(got) != 0 {
		t.Errorf("after forgetting it, the markers are %v", got)
	}
}
