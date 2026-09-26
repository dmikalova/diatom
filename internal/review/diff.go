// Package review is diatom's review record (ADR 0001): the hunks of each
// commit the agents made, the human's decision on each, and the queue of what
// is left to review. The reviewer pane only writes decisions; the scheduler
// turns rejections into revision tasks, because only the harness writes task
// files.
package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"

	"github.com/dmikalova/diatom/internal/git"
)

// Hunk is one hunk of one commit, the unit a review decision is made on.
type Hunk struct {
	// Commit is the full SHA of the commit the hunk belongs to.
	Commit string
	// ID names the hunk within its commit: the file's path and the hunk's
	// 1-based place in it, such as engine/ward.go#2.
	ID      string
	Path    string
	OldPath string
	// Index is the hunk's 1-based place in its file, of Of.
	Index, Of int
	// New and Deleted mark a file the commit added or removed.
	New, Deleted bool
	Fragment     *gitdiff.TextFragment
}

// diffArgs pin every option that shapes a diff, so a hunk's ID means the same
// thing however the human's git is configured.
var diffArgs = []string{
	"--format=", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/",
	"--diff-algorithm=myers", "-U3", "-M", "--no-relative",
}

// Hunks returns the hunks of a commit. A merge's hunks are what its committer
// changed from the automatic merge (git's remerge diff): the resolution of
// its conflicts, or the repair of a merge that failed the gate. A clean merge
// has none.
func Hunks(ctx context.Context, repo git.Repo, sha string) ([]Hunk, error) {
	args := append([]string{"show", "--remerge-diff"}, diffArgs...)
	out, err := repo.Run(ctx, append(args, sha)...)
	if err != nil {
		return nil, err
	}
	return parse(sha, out)
}

// Combined returns how the hunks of original look with a fixup folded in, as
// they will after `git rebase --autosquash` (ADR 0001). It applies the fixup's
// change to original's tree without touching any branch, and diffs the result
// against original's parent over the files original touched.
func Combined(ctx context.Context, repo git.Repo, original, fixup string) ([]Hunk, error) {
	tree, err := repo.Run(ctx, "merge-tree", "--write-tree", "--no-messages",
		"--merge-base="+fixup+"^", original, fixup)
	if err != nil {
		return nil, fmt.Errorf("fold %s into %s: %w", short(fixup), short(original), err)
	}
	tree, _, _ = strings.Cut(tree, "\n")
	paths, err := repo.Run(ctx, "diff-tree", "--no-commit-id", "--name-only", "-r", "-M", original)
	if err != nil {
		return nil, err
	}
	args := append([]string{"diff"}, diffArgs[1:]...)
	args = append(args, original+"^", tree, "--")
	out, err := repo.Run(ctx, append(args, strings.Fields(paths)...)...)
	if err != nil {
		return nil, err
	}
	return parse(original, out)
}

func parse(sha, diff string) ([]Hunk, error) {
	if strings.TrimSpace(diff) == "" {
		return nil, nil
	}
	files, _, err := gitdiff.Parse(strings.NewReader(diff + "\n"))
	if err != nil {
		return nil, fmt.Errorf("diff of %s: %w", short(sha), err)
	}
	var hunks []Hunk
	for _, f := range files {
		path := f.NewName
		if f.IsDelete {
			path = f.OldName
		}
		for i, frag := range f.TextFragments {
			hunks = append(hunks, Hunk{
				Commit: sha, ID: fmt.Sprintf("%s#%d", path, i+1),
				Path: path, OldPath: f.OldName, Index: i + 1, Of: len(f.TextFragments),
				New: f.IsNew, Deleted: f.IsDelete, Fragment: frag,
			})
		}
	}
	return hunks, nil
}

// Text renders the hunk as a unified diff, for a revision task.
func (h Hunk) Text() string {
	var b strings.Builder
	b.WriteString(h.Fragment.Header() + "\n")
	for _, l := range h.Fragment.Lines {
		b.WriteString(l.String())
	}
	return strings.TrimRight(b.String(), "\n")
}

// NewLine returns the line number in the new file of the hunk's line at
// index i, or in the old file for a deleted line.
func (h Hunk) NewLine(i int) int {
	oldN, newN := int(h.Fragment.OldPosition), int(h.Fragment.NewPosition)
	for j, l := range h.Fragment.Lines {
		if j == i {
			if l.Op == gitdiff.OpDelete {
				return oldN
			}
			return newN
		}
		if l.Op != gitdiff.OpAdd {
			oldN++
		}
		if l.Op != gitdiff.OpDelete {
			newN++
		}
	}
	return newN
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
