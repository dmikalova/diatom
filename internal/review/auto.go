package review

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/git"
)

// Match returns the first of patterns that path matches, or "". A pattern
// without a slash matches the file's name in any directory, such as
// *_test.go; one with a slash matches the whole path from the repo's root,
// and one ending in /** everything below that directory.
func Match(patterns []string, p string) string {
	for _, pat := range patterns {
		switch {
		case strings.HasSuffix(pat, "/**"):
			if strings.HasPrefix(p, strings.TrimSuffix(pat, "**")) {
				return pat
			}
		case strings.Contains(pat, "/"):
			if ok, _ := path.Match(pat, p); ok {
				return pat
			}
		default:
			if ok, _ := path.Match(pat, path.Base(p)); ok {
				return pat
			}
		}
	}
	return ""
}

// AutoApprove approves each hunk of commit sha that nobody has decided on
// and whose file one of patterns matches, and returns how many it approved.
// The approval names its pattern, and stays out of the history the human
// steps back through: it was no decision of theirs.
func AutoApprove(
	ctx context.Context,
	repo git.Repo,
	s Store,
	sha string,
	patterns []string,
	now time.Time,
) (int, error) {
	if len(patterns) == 0 {
		return 0, nil
	}
	hunks, err := Hunks(ctx, repo, sha)
	if err != nil {
		return 0, err
	}
	c, err := s.Load(sha)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, h := range hunks {
		pat := Match(patterns, h.Path)
		if pat == "" || c.Hunks[h.ID] != nil {
			continue
		}
		c.Hunks[h.ID] = &Record{Decision: Approve, At: now, Seq: 1, Auto: pat}
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.save(sha, c)
}
