// Package state is where diatom keeps what it knows about a repository,
// outside the repository itself (ADR 0013): one directory per repository,
// under a single base directory in the home directory, named for the
// repository's origin remote.
package state

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// Dir is where diatom keeps everything it knows, outside the repositories it
// works in (ADR 0013). It is the same directory the ledger and the window's
// layout sit in: one directory per repository under it, named for the
// repository's origin.
func Dir(home string) string { return config.Paths{Home: home}.StateDir() }

// MarkerFile holds the path of the repository a state directory is of, so
// the directories can be read back without a repository to ask (ADR 0013).
const MarkerFile = "repo"

// Key names a repository by its origin, lowercased, without a scheme, a
// user, a port or a .git suffix: github.com/goodship-io/nextjs. Two
// checkouts of one repository share a key, and so one queue.
func Key(remote string) (string, error) {
	s := strings.TrimSpace(remote)
	if s == "" {
		return "", fmt.Errorf("no remote to name the repo by")
	}
	s = strings.TrimSuffix(s, ".git")
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("the remote %q is no URL: %w", remote, err)
		}
		s = u.Hostname() + "/" + strings.Trim(u.Path, "/")
	case scpRe.MatchString(s):
		m := scpRe.FindStringSubmatch(s)
		s = m[1] + "/" + strings.TrimPrefix(m[2], "/")
	}
	s = strings.ToLower(strings.Trim(s, "/"))
	if !keyRe.MatchString(s) {
		return "", fmt.Errorf("the remote %q gives no usable name: %q", remote, s)
	}
	return s, nil
}

// scpRe matches git's scp-like remotes, such as git@github.com:owner/repo.
var scpRe = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// keyRe is what a key may hold: a host, which has a dot in it, and at least
// one path element after it. A remote that is only a path names no forge and
// so names no repository diatom can tell from another.
var keyRe = regexp.MustCompile(`^[a-z0-9_-]+(\.[a-z0-9_-]+)+(/[a-z0-9._+-]+)+$`)

// Open returns the store of the repository rooted at repo, in base. It makes
// the directory and writes its marker the first time.
//
// A repository whose origin names a key no directory has, while exactly one
// directory's marker points at this very path, is one that was renamed or
// moved on the forge: its directory is renamed to the new key and kept, so
// the goals in it carry on.
func Open(ctx context.Context, base, repo string) (*queue.Store, error) {
	remote, err := origin(ctx, repo)
	if err != nil {
		return nil, err
	}
	key, err := Key(remote)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	dir := filepath.Join(base, key)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		adopt(base, repo, key)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeMarker(dir, repo); err != nil {
		return nil, err
	}
	return queue.At(repo, dir, key), nil
}

// adopt renames the state directory whose marker alone points at repo to
// key. It does nothing when none does, or when more than one does and
// renaming could take the wrong one.
func adopt(base, repo, key string) {
	var found []string
	for dir, at := range Markers(base) {
		if at == repo {
			found = append(found, dir)
		}
	}
	if len(found) != 1 {
		return
	}
	dst := filepath.Join(base, key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	_ = os.Rename(filepath.Join(base, found[0]), dst)
}

// Markers reads every state directory under base and the repository path
// each one says it is of, keyed by the directory's name under base.
func Markers(base string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != MarkerFile {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(base, filepath.Dir(path))
		if err != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = string(bytes.TrimSpace(b))
		return nil
	})
	return out
}

// Orphans are the state directories whose repository is gone from the path
// their marker gives, keyed by the directory's name under base. Diatom asks
// the human what became of each one rather than guess (ADR 0013).
func Orphans(base string) map[string]string {
	out := map[string]string{}
	for dir, repo := range Markers(base) {
		if repo == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(repo, ".git")); err != nil || fi == nil {
			out[dir] = repo
		}
	}
	return out
}

func writeMarker(dir, repo string) error {
	path := filepath.Join(dir, MarkerFile)
	if b, err := os.ReadFile(path); err == nil && string(bytes.TrimSpace(b)) == repo {
		return nil
	}
	return os.WriteFile(path, []byte(repo+"\n"), 0o644)
}

// Repoint says where the repository of the state directory dir, under base,
// is now that it has moved on disk, and tells git where the worktrees in it
// are (ADR 0013).
func Repoint(ctx context.Context, base, dir, repo string) error {
	root, err := under(base, dir)
	if err != nil {
		return err
	}
	repo = filepath.Clean(strings.TrimSpace(repo))
	if !filepath.IsAbs(repo) {
		return fmt.Errorf("%s is no absolute path", repo)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return fmt.Errorf("there is no repository at %s", repo)
	}
	if err := writeMarker(root, repo); err != nil {
		return err
	}
	return repair(ctx, repo, root)
}

// Forget throws away everything diatom keeps about one repository: the
// state directory dir under base, worktrees and all.
func Forget(base, dir string) error {
	root, err := under(base, dir)
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

// under is the path of the state directory dir, which must be a directory
// under base and not base itself.
func under(base, dir string) (string, error) {
	root := filepath.Join(base, dir)
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || rel == string(filepath.Separator) ||
		strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%q names no state directory of %s", dir, base)
	}
	return root, nil
}

// origin is the URL of the repository's origin remote.
func origin(ctx context.Context, repo string) (string, error) {
	out, err := (git.Repo{Dir: repo}).Run(ctx, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("%s has no origin remote: %w", repo, err)
	}
	return strings.TrimSpace(out), nil
}
