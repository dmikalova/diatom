package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// Migrate moves a repository's old `.diatom/` directory out to base and
// repairs the worktrees that were in it, so a repository diatom used before
// ADR 0013 opens where every other one does.
//
// It refuses while the repository's scheduler holds its lock, or while a
// session has work the harness has not settled: moving under a running
// scheduler would leave both halves wrong. It reports whether it moved
// anything.
func Migrate(ctx context.Context, base, repo string) (bool, error) {
	old := filepath.Join(repo, ".diatom")
	if fi, err := os.Stat(old); err != nil || !fi.IsDir() {
		return false, nil
	}
	if err := idle(old); err != nil {
		return false, err
	}
	remote, err := origin(ctx, repo)
	if err != nil {
		return false, err
	}
	key, err := Key(remote)
	if err != nil {
		return false, fmt.Errorf("%s: %w", repo, err)
	}
	dst := filepath.Join(base, key)
	if _, err := os.Stat(dst); err == nil {
		return false, fmt.Errorf(
			"%s was already moved to %s, but %s is still there: merge them by hand", repo, dst, old)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(old, dst); err != nil {
		return false, fmt.Errorf("moving %s to %s: %w", old, dst, err)
	}
	if err := writeMarker(dst, repo); err != nil {
		return false, err
	}
	if err := repair(ctx, repo, dst); err != nil {
		return true, err
	}
	return true, nil
}

// idle reports an error when a scheduler is running on the store, or a
// session of it has not settled.
func idle(root string) error {
	if _, err := (&queue.Store{Root: root}).Scheduler(); err == nil {
		return errors.New("a diatom scheduler is running here: stop it and open it again")
	} else if !errors.Is(err, queue.ErrNotRunning) {
		return err
	}
	goals, err := os.ReadDir(filepath.Join(root, "goals"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, g := range goals {
		if !g.IsDir() {
			continue
		}
		sessions, err := os.ReadDir(filepath.Join(root, "goals", g.Name(), "sessions"))
		if err != nil {
			continue
		}
		for _, s := range sessions {
			dir := filepath.Join(root, "goals", g.Name(), "sessions", s.Name())
			if _, err := os.Stat(filepath.Join(dir, "spec.json")); err != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, "result.json")); os.IsNotExist(err) {
				return fmt.Errorf(
					"the session %s of %s hasn't settled: let it finish, then open it again",
					s.Name(), g.Name())
			}
		}
	}
	return nil
}

// repair tells git where the worktrees that moved with the state are now.
// One call takes all of them.
func repair(ctx context.Context, repo, root string) error {
	var paths []string
	goals, err := os.ReadDir(filepath.Join(root, "goals"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, g := range goals {
		if !g.IsDir() {
			continue
		}
		wts := filepath.Join(root, "goals", g.Name(), "worktrees")
		entries, err := os.ReadDir(wts)
		if err != nil {
			continue
		}
		for _, w := range entries {
			if w.IsDir() {
				paths = append(paths, filepath.Join(wts, w.Name()))
			}
		}
	}
	if landing := filepath.Join(root, "landing"); isDir(landing) {
		paths = append(paths, landing)
	}
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"worktree", "repair"}, paths...)
	if _, err := (git.Repo{Dir: repo}).Run(ctx, args...); err != nil {
		return fmt.Errorf("telling git where the worktrees moved to: %w", err)
	}
	return nil
}

// Unignore takes the line that ignored `.diatom/` out of the repository's
// own .gitignore, now that nothing of diatom is in the repository.
func Unignore(repo string) error {
	path := filepath.Join(repo, ".gitignore")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var kept []string
	dropped := false
	for line := range strings.SplitSeq(string(b), "\n") {
		switch strings.TrimSpace(line) {
		case ".diatom", ".diatom/", "/.diatom", "/.diatom/":
			dropped = true
		default:
			kept = append(kept, line)
		}
	}
	if !dropped {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644)
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
