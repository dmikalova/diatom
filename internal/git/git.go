// Package git runs the git operations the harness owns (ADR 0005): agents
// only edit files, and every branch, worktree, merge and commit goes through
// here.
//
// Branches follow ADR 0003. A goal's integration branch collects every commit
// that passes the gate, merged in by MergeInto without a worktree of its own.
// Each workstream commits in its own worktree and merges the integration
// branch in before each task. Nothing is ever rebased, because commit SHAs
// anchor review decisions.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Repo is a git working tree: the main one or a linked worktree.
type Repo struct {
	Dir string
}

// Run runs git in the working tree and returns its trimmed stdout. A failure
// carries git's stderr.
func (r Repo) Run(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, nil, nil, args...)
}

func (r Repo) run(
	ctx context.Context,
	env []string,
	stdin io.Reader,
	args ...string,
) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = stdin
	cmd.Env = append(isolatedEnv(os.Environ()), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(
			stdout.String(),
		), &Error{
			Args:   args,
			Err:    err,
			Stderr: strings.TrimSpace(stderr.String()),
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// locating are the variables that point git at a repository other than the
// one its working directory is in. Git sets them for its own hooks, so a
// diatom command run from a hook, or the tests run by a pre-commit hook,
// would otherwise act on the repository being committed to.
var locating = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX",
}

// isolatedEnv drops the locating variables from env, so each Repo acts only
// on its own directory.
func isolatedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(locating, name) {
			out = append(out, kv)
		}
	}
	return out
}

// Error is a failed git command.
type Error struct {
	Args   []string
	Err    error
	Stderr string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Err }

// exitCode is the exit status of a failed git command, or -1.
func exitCode(err error) int {
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// Root returns the top of the main working tree that dir belongs to, even
// when dir is inside a linked worktree.
func Root(ctx context.Context, dir string) (string, error) {
	common, err := Repo{dir}.Run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Dir(common), nil
}

// CurrentBranch returns the checked-out branch.
func (r Repo) CurrentBranch(ctx context.Context) (string, error) {
	return r.Run(ctx, "symbolic-ref", "--short", "HEAD")
}

// RevParse resolves a revision to its object name.
func (r Repo) RevParse(ctx context.Context, rev string) (string, error) {
	return r.Run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
}

// BranchExists reports whether the local branch exists.
func (r Repo) BranchExists(ctx context.Context, branch string) bool {
	_, err := r.Run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// CreateBranch creates branch at start unless it already exists.
func (r Repo) CreateBranch(ctx context.Context, branch, start string) error {
	if r.BranchExists(ctx, branch) {
		return nil
	}
	_, err := r.Run(ctx, "branch", branch, start)
	return err
}

// EnsureWorktree checks branch out at path, creating the branch from start
// first if needed. An existing worktree at path is left as it is.
func (r Repo) EnsureWorktree(ctx context.Context, path, branch, start string) error {
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return nil
	}
	if err := r.CreateBranch(ctx, branch, start); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := r.Run(ctx, "worktree", "add", path, branch)
	return err
}

// IsIgnored reports whether path is ignored by the repo's excludes, including
// the global excludes file ADR 0002 relies on.
func (r Repo) IsIgnored(ctx context.Context, path string) bool {
	_, err := r.Run(ctx, "check-ignore", "--quiet", "--no-index", path)
	return err == nil
}

// IsAncestor reports whether a is an ancestor of, or the same commit as, b.
func (r Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.Run(ctx, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// MergeResult is how a merge went.
type MergeResult int

// The merge results.
const (
	// UpToDate means there was nothing to merge.
	UpToDate MergeResult = iota
	// Merged means the merge applied cleanly.
	Merged
	// Conflicted means the merge left conflicts to resolve.
	Conflicted
)

// MergeNoCommit merges ref into the checked-out branch and stops before
// committing, so the harness can run the gate on the result first. On
// Merged or Conflicted a merge is in progress; commit it with CommitAll or
// undo it with AbortMerge.
func (r Repo) MergeNoCommit(ctx context.Context, ref string) (MergeResult, error) {
	if up, err := r.IsAncestor(ctx, ref, "HEAD"); err != nil || up {
		return UpToDate, err
	}
	_, err := r.Run(ctx, "merge", "--no-ff", "--no-commit", "--no-edit", ref)
	if err == nil {
		return Merged, nil
	}
	if r.MergeInProgress(ctx) {
		return Conflicted, nil
	}
	return UpToDate, err
}

// MergeInProgress reports whether a merge is waiting to be committed.
func (r Repo) MergeInProgress(ctx context.Context) bool {
	_, err := r.Run(ctx, "rev-parse", "--quiet", "--verify", "MERGE_HEAD")
	return err == nil
}

// AbortMerge undoes a merge in progress.
func (r Repo) AbortMerge(ctx context.Context) error {
	_, err := r.Run(ctx, "merge", "--abort")
	return err
}

// MergeInto merges source into the branch target without a worktree, and
// reports whether it conflicted, in which case target is left as it was.
// When target has not moved since source last merged it, target simply fast
// forwards to source.
func (r Repo) MergeInto(ctx context.Context, target, source string) (conflicted bool, err error) {
	targetSHA, err := r.RevParse(ctx, target)
	if err != nil {
		return false, err
	}
	sourceSHA, err := r.RevParse(ctx, source)
	if err != nil {
		return false, err
	}
	if up, err := r.IsAncestor(ctx, sourceSHA, targetSHA); err != nil || up {
		return false, err
	}
	next := sourceSHA
	if ff, err := r.IsAncestor(ctx, targetSHA, sourceSHA); err != nil {
		return false, err
	} else if !ff {
		tree, err := r.Run(ctx, "merge-tree", "--write-tree", "--no-messages", targetSHA, sourceSHA)
		if exitCode(err) == 1 {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		tree, _, _ = strings.Cut(tree, "\n")
		msg := fmt.Sprintf("Merge branch '%s' into %s", source, target)
		next, err = r.Run(ctx, "commit-tree", tree, "-p", targetSHA, "-p", sourceSHA, "-m", msg)
		if err != nil {
			return false, err
		}
	}
	// The old value makes the update fail rather than lose a commit that
	// landed on target in the meantime.
	_, err = r.Run(ctx, "update-ref", "refs/heads/"+target, next, targetSHA)
	return false, err
}

// Dirty reports whether the worktree has any change, untracked files
// included.
func (r Repo) Dirty(ctx context.Context) (bool, error) {
	out, err := r.Run(ctx, "status", "--porcelain", "--untracked-files=all")
	return out != "", err
}

// Fingerprint returns the tree the worktree would commit right now, without
// touching the real index. Two equal fingerprints mean the files are the same,
// so the Stop hook and the harness can skip running the gate twice on them.
func (r Repo) Fingerprint(ctx context.Context) (string, error) {
	var tree string
	err := r.withTempIndex(ctx, func(env []string) error {
		if _, err := r.run(ctx, env, nil, "add", "--all"); err != nil {
			return err
		}
		var err error
		tree, err = r.run(ctx, env, nil, "write-tree")
		return err
	})
	return tree, err
}

// HeadTree returns the tree of HEAD.
func (r Repo) HeadTree(ctx context.Context) (string, error) {
	return r.Run(ctx, "rev-parse", "HEAD^{tree}")
}

// ConflictMarkers lists the lines that still carry a conflict marker in the
// worktree, compared with HEAD.
func (r Repo) ConflictMarkers(ctx context.Context) ([]string, error) {
	var markers []string
	err := r.withTempIndex(ctx, func(env []string) error {
		if _, err := r.run(ctx, env, nil, "add", "--all"); err != nil {
			return err
		}
		// --check also reports whitespace errors, which aren't ours to judge.
		out, err := r.run(ctx, env, nil, "diff", "--cached", "--check", "HEAD")
		if err != nil && exitCode(err) != 2 {
			return err
		}
		for line := range strings.SplitSeq(out, "\n") {
			if strings.Contains(line, "leftover conflict marker") {
				markers = append(markers, line)
			}
		}
		return nil
	})
	return markers, err
}

// withTempIndex runs fn with GIT_INDEX_FILE pointing at a copy of the
// worktree's index. Copying rather than starting empty keeps git's stat cache,
// so large trees aren't rehashed.
func (r Repo) withTempIndex(ctx context.Context, fn func(env []string) error) error {
	index, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "diatom-index-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	src, err := os.ReadFile(index)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A merge in progress leaves conflict stages in the index, which
	// write-tree refuses; `add --all` resolves them in the copy.
	if _, err := tmp.Write(src); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Git trusts a cached entry only when the file is older than the index
	// itself, and rereads the "racily clean" rest. A fresh copy is newer
	// than every file, so it would trust a same-size edit made in the same
	// second as the last commit and miss it; the copy keeps the original's
	// time instead. TestFingerprintSeesRacyEdit pins this.
	if info, err := os.Stat(index); err == nil {
		if err := os.Chtimes(tmp.Name(), info.ModTime(), info.ModTime()); err != nil {
			return err
		}
	}
	return fn([]string{"GIT_INDEX_FILE=" + tmp.Name()})
}

// StageAll stages every change, untracked files included, and reports
// whether anything is staged against HEAD.
func (r Repo) StageAll(ctx context.Context) (bool, error) {
	if _, err := r.Run(ctx, "add", "--all"); err != nil {
		return false, err
	}
	_, err := r.Run(ctx, "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	if exitCode(err) == 1 {
		return true, nil
	}
	return false, err
}

// StagedDiff returns the staged diff with its stat.
func (r Repo) StagedDiff(ctx context.Context) (stat, diff string, err error) {
	if stat, err = r.Run(ctx, "diff", "--cached", "--stat"); err != nil {
		return "", "", err
	}
	diff, err = r.Run(ctx, "diff", "--cached")
	return stat, diff, err
}

// Commit commits what is staged with message and returns the new commit.
// The repo's own hooks are skipped: the harness has already run the gate and
// checked the message itself (ADR 0005).
func (r Repo) Commit(ctx context.Context, message string) (string, error) {
	if _, err := r.run(
		ctx,
		nil,
		strings.NewReader(message),
		"commit",
		"--no-verify",
		"--file=-",
	); err != nil {
		return "", err
	}
	return r.RevParse(ctx, "HEAD")
}

// CommitMerge stages everything and commits the merge in progress with git's
// default message.
func (r Repo) CommitMerge(ctx context.Context) (string, error) {
	if _, err := r.Run(ctx, "add", "--all"); err != nil {
		return "", err
	}
	if _, err := r.Run(ctx, "commit", "--no-verify", "--no-edit"); err != nil {
		return "", err
	}
	return r.RevParse(ctx, "HEAD")
}

// WriteTree writes the index as a tree and returns it.
func (r Repo) WriteTree(ctx context.Context) (string, error) {
	return r.Run(ctx, "write-tree")
}

// Subject returns a commit's subject line.
func (r Repo) Subject(ctx context.Context, rev string) (string, error) {
	return r.Run(ctx, "show", "-s", "--format=%s", rev)
}

// CommitTree makes a commit of tree on top of HEAD, without the index or the
// hooks, and moves the checked-out branch to it. The old value makes the move
// fail rather than lose a commit made in the meantime.
func (r Repo) CommitTree(ctx context.Context, tree, message string) (string, error) {
	head, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		return "", err
	}
	sha, err := r.run(
		ctx,
		nil,
		strings.NewReader(message),
		"commit-tree",
		tree,
		"-p",
		head,
		"-F",
		"-",
	)
	if err != nil {
		return "", err
	}
	_, err = r.Run(ctx, "update-ref", "HEAD", sha, head)
	return sha, err
}

// Stash saves every change, untracked files included, and cleans the
// worktree. The work stays reachable in the stash list.
func (r Repo) Stash(ctx context.Context, message string) error {
	_, err := r.Run(ctx, "stash", "push", "--include-untracked", "--message", message)
	return err
}
