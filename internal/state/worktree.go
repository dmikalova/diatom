package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dmikalova/diatom/internal/git"
)

// repair tells git where the worktrees of a state directory are, after the
// repository they are of has moved. One call takes all of them.
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

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
