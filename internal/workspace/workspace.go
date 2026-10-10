// Package workspace is the repos one diatom window works in (ADR 0007).
// Opened inside a git repo, that is the one repo. Opened outside one, it is
// every git repo directly under the directory, so a window over a directory
// of repos is opened by opening the directory.
package workspace

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/state"
)

// Workspace is the repos a window works in.
type Workspace struct {
	// Root is the directory the window is over: the repo itself when there
	// is one, else the directory holding them.
	Root string
	// Repos are the stores, by path, one per repo.
	Repos []*queue.Store
	// Skipped names the repos the window left out and says why, such as a
	// repo with no origin to name its state by (ADR 0013).
	Skipped map[string]string
}

// Open finds the repos to work in from dir, anywhere inside a repo, a goal's
// worktree included. Outside a repo, every directory directly under dir that
// is a repo is one: nothing deeper, so a directory of directories of repos
// opens nothing. A repo diatom cannot keep state for is left out and said
// so, rather than closing the window on the rest.
func Open(ctx context.Context, dir string) (*Workspace, error) {
	if root, err := git.Root(ctx, dir); err == nil {
		return build(ctx, root, []string{root})
	}
	repos, err := under(dir)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf(
			"%s is not a git repository and holds none: diatom works in a repo, or in a "+
				"directory of repos", dir)
	}
	return build(ctx, dir, repos)
}

// under is the directories directly under dir that are git repositories.
func under(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var repos []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			repos = append(repos, path)
		}
	}
	slices.Sort(repos)
	return repos, nil
}

// build makes the workspace. Diatom's state is outside the repos now (ADR
// 0013), so each one is moved out first if it still holds a `.diatom/`, and
// keyed by its origin. A repo with no origin, and a second checkout of one
// already in the workspace, are left out and said so.
func build(ctx context.Context, root string, repos []string) (*Workspace, error) {
	w := &Workspace{Root: root, Skipped: map[string]string{}}
	base := state.Dir(home())
	taken := map[string]string{}
	for _, repo := range repos {
		if _, err := state.Migrate(ctx, base, repo); err != nil {
			w.Skipped[repo] = err.Error()
			continue
		}
		s, err := state.Open(ctx, base, repo)
		if err != nil {
			w.Skipped[repo] = err.Error()
			continue
		}
		if first, ok := taken[s.Root]; ok {
			w.Skipped[repo] = "the same repository as " + first +
				", which diatom already works in: one checkout at a time"
			continue
		}
		taken[s.Root] = repo
		w.Repos = append(w.Repos, s)
	}
	if len(w.Repos) == 0 {
		return nil, fmt.Errorf("diatom can work in none of these repos:\n%s", why(w.Skipped))
	}
	return w, nil
}

// why lists the repos left out and the reason for each.
func why(skipped map[string]string) string {
	var b strings.Builder
	for _, repo := range slices.Sorted(maps.Keys(skipped)) {
		fmt.Fprintf(&b, "  %s: %s\n", repo, skipped[repo])
	}
	return b.String()
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// One is the store when the workspace holds a single repo, and nil when it
// holds more.
func (w *Workspace) One() *queue.Store {
	if len(w.Repos) == 1 {
		return w.Repos[0]
	}
	return nil
}

// Store is the store of the repo rooted at repo, and nil when the workspace
// doesn't hold it.
func (w *Workspace) Store(repo string) *queue.Store {
	for _, s := range w.Repos {
		if s.Repo() == repo {
			return s
		}
	}
	return nil
}

// Name is what a repo is called in the window: its directory's name.
func Name(s *queue.Store) string { return filepath.Base(s.Repo()) }
