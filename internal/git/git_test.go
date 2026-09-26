package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRepo makes a repo with one commit on main.
func newRepo(t *testing.T) Repo {
	t.Helper()
	r := Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		mustRun(t, r, args...)
	}
	writeFile(t, r, "a.txt", "one\n")
	commitAll(t, r, "chore: start")
	return r
}

func mustRun(t *testing.T, r Repo, args ...string) string {
	t.Helper()
	out, err := r.Run(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, r Repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, r Repo, msg string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestWorktreeAndBranches(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	branch, err := r.CurrentBranch(ctx)
	if err != nil || branch != "main" {
		t.Fatalf("CurrentBranch = %q, %v", branch, err)
	}
	if err := r.CreateBranch(ctx, "g/integration", "main"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(r.Dir, ".diatom", "wt")
	if err := r.EnsureWorktree(ctx, wt, "g/ws/engine", "g/integration"); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureWorktree(ctx, wt, "g/ws/engine", "g/integration"); err != nil {
		t.Fatalf("EnsureWorktree on an existing worktree: %v", err)
	}
	w := Repo{Dir: wt}
	if b, _ := w.CurrentBranch(ctx); b != "g/ws/engine" {
		t.Errorf("worktree branch = %q", b)
	}
	root, err := Root(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(r.Dir)
	if got, _ := filepath.EvalSymlinks(root); got != want {
		t.Errorf("Root from the worktree = %q, want %q", got, want)
	}
	if !r.BranchExists(ctx, "g/ws/engine") || r.BranchExists(ctx, "nope") {
		t.Error("BranchExists is wrong")
	}
}

func TestMergeIntoFastForwardsAndMerges(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	mustRun(t, r, "branch", "integration")
	mustRun(t, r, "checkout", "-q", "-b", "a")
	writeFile(t, r, "b.txt", "b\n")
	a := commitAll(t, r, "feat: b")

	if conflicted, err := r.MergeInto(ctx, "integration", "a"); err != nil || conflicted {
		t.Fatalf("MergeInto = %v, %v", conflicted, err)
	}
	if got, _ := r.RevParse(ctx, "integration"); got != a {
		t.Errorf("integration = %s, want fast-forwarded to %s", got, a)
	}
	if conflicted, err := r.MergeInto(ctx, "integration", "a"); err != nil || conflicted {
		t.Fatalf("MergeInto with nothing new = %v, %v", conflicted, err)
	}

	// A second branch off the old main now needs a real merge.
	mustRun(t, r, "checkout", "-q", "-b", "c", "main")
	writeFile(t, r, "c.txt", "c\n")
	c := commitAll(t, r, "feat: c")
	if conflicted, err := r.MergeInto(ctx, "integration", "c"); err != nil || conflicted {
		t.Fatalf("MergeInto = %v, %v", conflicted, err)
	}
	parents := mustRun(t, r, "rev-list", "--parents", "-n", "1", "integration")
	if !strings.Contains(parents, a) || !strings.Contains(parents, c) {
		t.Errorf("integration's parents = %s, want %s and %s", parents, a, c)
	}
	if files := mustRun(
		t,
		r,
		"ls-tree",
		"--name-only",
		"integration",
	); files != "a.txt\nb.txt\nc.txt" {
		t.Errorf("integration tree = %q", files)
	}
}

func TestMergeIntoConflict(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	mustRun(t, r, "checkout", "-q", "-b", "integration")
	writeFile(t, r, "a.txt", "integration\n")
	before := commitAll(t, r, "feat: i")
	mustRun(t, r, "checkout", "-q", "-b", "ws", "main")
	writeFile(t, r, "a.txt", "ws\n")
	commitAll(t, r, "feat: ws")
	conflicted, err := r.MergeInto(ctx, "integration", "ws")
	if err != nil || !conflicted {
		t.Fatalf("MergeInto = %v, %v, want a conflict", conflicted, err)
	}
	if got, _ := r.RevParse(ctx, "integration"); got != before {
		t.Error("a conflicted MergeInto moved the target")
	}
}

func TestMergeNoCommit(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	mustRun(t, r, "checkout", "-q", "-b", "other")
	writeFile(t, r, "a.txt", "other\n")
	commitAll(t, r, "feat: other")
	mustRun(t, r, "checkout", "-q", "main")

	if res, err := r.MergeNoCommit(ctx, "main"); err != nil || res != UpToDate {
		t.Errorf("MergeNoCommit of HEAD = %v, %v", res, err)
	}

	mustRun(t, r, "checkout", "-q", "-b", "clean")
	writeFile(t, r, "new.txt", "x\n")
	commitAll(t, r, "feat: new")
	res, err := r.MergeNoCommit(ctx, "other")
	if err != nil || res != Merged || !r.MergeInProgress(ctx) {
		t.Fatalf("clean MergeNoCommit = %v, %v, in progress %v", res, err, r.MergeInProgress(ctx))
	}
	if _, err := r.CommitMerge(ctx); err != nil {
		t.Fatal(err)
	}
	if r.MergeInProgress(ctx) {
		t.Error("merge still in progress after CommitMerge")
	}

	mustRun(t, r, "checkout", "-q", "-b", "fight", "main")
	writeFile(t, r, "a.txt", "fight\n")
	commitAll(t, r, "feat: fight")
	res, err = r.MergeNoCommit(ctx, "other")
	if err != nil || res != Conflicted {
		t.Fatalf("conflicting MergeNoCommit = %v, %v", res, err)
	}
	markers, err := r.ConflictMarkers(ctx)
	if err != nil || len(markers) == 0 {
		t.Errorf("ConflictMarkers = %v, %v, want the conflict's markers", markers, err)
	}
	fp, err := r.Fingerprint(ctx)
	if err != nil || fp == "" {
		t.Errorf("Fingerprint during a conflict = %q, %v", fp, err)
	}
	writeFile(t, r, "a.txt", "resolved\n")
	if markers, _ := r.ConflictMarkers(ctx); len(markers) != 0 {
		t.Errorf("ConflictMarkers after resolving = %v", markers)
	}
	if err := r.AbortMerge(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintAndDirty(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	head, _ := r.HeadTree(ctx)
	fp, err := r.Fingerprint(ctx)
	if err != nil || fp != head {
		t.Errorf("clean Fingerprint = %s, want HEAD's tree %s (%v)", fp, head, err)
	}
	if dirty, _ := r.Dirty(ctx); dirty {
		t.Error("clean worktree is dirty")
	}
	writeFile(t, r, "untracked.txt", "u\n")
	fp2, _ := r.Fingerprint(ctx)
	if fp2 == head {
		t.Error("Fingerprint ignores an untracked file")
	}
	if dirty, _ := r.Dirty(ctx); !dirty {
		t.Error("Dirty misses an untracked file")
	}
	if status := mustRun(t, r, "status", "--porcelain"); status != "?? untracked.txt" {
		t.Errorf("Fingerprint touched the real index: status %q", status)
	}

	staged, err := r.StageAll(ctx)
	if err != nil || !staged {
		t.Fatalf("StageAll = %v, %v", staged, err)
	}
	stat, diff, err := r.StagedDiff(ctx)
	if err != nil || !strings.Contains(stat, "untracked.txt") || !strings.Contains(diff, "+u") {
		t.Errorf("StagedDiff = %q, %q, %v", stat, diff, err)
	}
	if err := r.Stash(ctx, "diatom: test"); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := r.Dirty(ctx); dirty {
		t.Error("worktree dirty after Stash")
	}
	if staged, _ := r.StageAll(ctx); staged {
		t.Error("StageAll of a clean tree reported staged changes")
	}
}

func TestIsIgnored(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	writeFile(t, r, ".gitignore", ".diatom/\n")
	if !r.IsIgnored(ctx, ".diatom/") {
		t.Error(".diatom/ not reported ignored")
	}
	if r.IsIgnored(ctx, "a.txt") {
		t.Error("a.txt reported ignored")
	}
}

func TestErrorCarriesStderr(t *testing.T) {
	r := newRepo(t)
	_, err := r.Run(context.Background(), "rev-parse", "--verify", "nope")
	if err == nil || !strings.Contains(err.Error(), "git rev-parse --verify nope") {
		t.Errorf("error = %v", err)
	}
}

// TestIgnoresLocatingEnv pins that a Repo acts on its own directory even
// when git's hook variables point elsewhere, as they do when a pre-commit
// hook runs these tests. The decoy is a throwaway repo, never a real one.
func TestIgnoresLocatingEnv(t *testing.T) {
	ctx := context.Background()
	decoy := newRepo(t)
	before := mustRun(t, decoy, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(decoy.Dir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy.Dir, ".git", "index"))
	t.Setenv("GIT_WORK_TREE", decoy.Dir)

	r := newRepo(t)
	writeFile(t, r, "b.txt", "b\n")
	commitAll(t, r, "feat: b")
	if got := mustRun(t, decoy, "rev-parse", "HEAD"); got != before {
		t.Fatalf("the decoy moved from %s to %s", before, got)
	}
	if files, _ := r.Run(ctx, "ls-tree", "--name-only", "HEAD"); files != "a.txt\nb.txt" {
		t.Errorf("own repo tree = %q", files)
	}
}

// TestFingerprintSeesRacyEdit pins that an edit which keeps a file's size and
// lands in the same second as the index write still changes the fingerprint,
// even when the fingerprint is taken seconds later. Git's cache would call the
// file unchanged unless the temp index keeps the real index's time.
func TestFingerprintSeesRacyEdit(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	head, _ := r.HeadTree(ctx)
	index, err := os.Stat(filepath.Join(r.Dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, r, "a.txt", "two\n")
	if err := os.Chtimes(
		filepath.Join(r.Dir, "a.txt"),
		index.ModTime(),
		index.ModTime(),
	); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	fp, err := r.Fingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fp == head {
		t.Fatal("a same-size edit in the index's second left the fingerprint at HEAD's tree")
	}
}

func TestCommitFiles(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	mustRun(t, r, "branch", "integration")
	sha, err := r.CommitFiles(
		ctx,
		"integration",
		map[string][]byte{"docs/adr/0001-x.md": []byte("# X\n")},
		"docs: x\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.RevParse(ctx, "integration"); got != sha {
		t.Errorf("integration = %s, want %s", got, sha)
	}
	if files := mustRun(
		t,
		r,
		"ls-tree",
		"-r",
		"--name-only",
		"integration",
	); files != "a.txt\ndocs/adr/0001-x.md" {
		t.Errorf("tree = %q, want the old file kept and the new one added", files)
	}
	if status := mustRun(t, r, "status", "--porcelain"); status != "" {
		t.Errorf("CommitFiles touched the worktree: %q", status)
	}
}

func TestEnsureDetached(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	path := filepath.Join(t.TempDir(), "planning")
	if err := r.EnsureDetached(ctx, path, "main"); err != nil {
		t.Fatal(err)
	}
	w := Repo{Dir: path}
	writeFile(t, w, "a.txt", "scribbled\n")
	writeFile(t, w, "junk.txt", "junk\n")
	writeFile(t, r, "a.txt", "two\n")
	next := commitAll(t, r, "feat: two")
	if err := r.EnsureDetached(ctx, path, "main"); err != nil {
		t.Fatal(err)
	}
	if head, _ := w.RevParse(ctx, "HEAD"); head != next {
		t.Errorf("planning worktree at %s, want %s", head, next)
	}
	if dirty, _ := w.Dirty(ctx); dirty {
		t.Error("the planning worktree kept what was written in it")
	}
}
