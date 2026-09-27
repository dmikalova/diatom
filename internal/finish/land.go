package finish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// GH runs the GitHub CLI in dir and returns its trimmed stdout.
type GH func(ctx context.Context, dir string, args ...string) (string, error)

// RunGH runs gh from PATH.
func RunGH(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err,
			string(bytes.TrimSpace(exitErr.Stderr)))
	}
	return string(bytes.TrimSpace(out)), err
}

// push pushes the laid-out goal straight to the base branch on remote. It
// never forces: when the remote's base has moved on, the push fails, and the
// goal has to be laid out again on top of it.
func push(ctx context.Context, s *queue.Store, g *queue.Goal, res *Result, remote string) error {
	repo := git.Repo{Dir: s.Repo()}
	_, err := repo.Run(ctx, "push", remote, res.Tip()+":refs/heads/"+g.Base)
	if gitErr := (*git.Error)(nil); errors.As(err, &gitErr) &&
		strings.Contains(gitErr.Stderr, "non-fast-forward") {
		return fmt.Errorf("%s on %s moved on while goal %s was being prepared: merge it again",
			g.Base, remote, g.Name)
	}
	return err
}

// OpenPRs pushes each pull request's branch to remote and opens a pull
// request for it on top of the one before, the first on the base branch. A
// pull request already open for a branch is kept, and pointed at the branch
// before it again. It returns each pull request's URL.
func OpenPRs(
	ctx context.Context,
	s *queue.Store,
	g *queue.Goal,
	res *Result,
	remote string,
	gh GH,
) ([]string, error) {
	repo := git.Repo{Dir: s.Repo()}
	var urls []string
	for i, pr := range res.Stack {
		// The branches are diatom's own, rewritten each time the goal is laid
		// out; the lease still keeps a commit someone pushed to one.
		if _, err := repo.Run(
			ctx,
			"push",
			"--force-with-lease",
			remote,
			pr.Tip+":refs/heads/"+pr.Branch,
		); err != nil {
			return urls, err
		}
		base := g.Base
		if i > 0 {
			base = res.Stack[i-1].Branch
		}
		if url, err := gh(
			ctx,
			repo.Dir,
			"pr",
			"view",
			pr.Branch,
			"--json",
			"url",
			"--jq",
			".url",
		); err == nil &&
			url != "" {
			if _, err := gh(ctx, repo.Dir, "pr", "edit", pr.Branch, "--base", base); err != nil {
				return urls, err
			}
			urls = append(urls, url)
			continue
		}
		out, err := gh(
			ctx,
			repo.Dir,
			"pr",
			"create",
			"--head",
			pr.Branch,
			"--base",
			base,
			"--title",
			prTitle(g, res, i),
			"--body",
			prBody(g, res, i, urls),
		)
		if err != nil {
			return urls, err
		}
		lines := strings.Split(out, "\n")
		urls = append(urls, lines[len(lines)-1])
	}
	return urls, nil
}

func prTitle(g *queue.Goal, res *Result, i int) string {
	if len(res.Stack) == 1 {
		return g.Title
	}
	return fmt.Sprintf("%s (%d/%d: %s)", g.Title, i+1, len(res.Stack), res.Stack[i].Workstream)
}

func prBody(g *queue.Goal, res *Result, i int, before []string) string {
	var b strings.Builder
	pr := res.Stack[i]
	if len(res.Stack) > 1 {
		fmt.Fprintf(&b, "The %s workstream of goal %s, pull request %d of %d.",
			pr.Workstream, g.Name, i+1, len(res.Stack))
		if i > 0 {
			fmt.Fprintf(&b, " It is stacked on %s: merge that first.", before[i-1])
		}
		b.WriteString("\n\n")
	}
	b.WriteString("Commits:\n\n")
	for _, c := range pr.Commits {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Summary says in one line how far a done goal is on its way upstream.
func Summary(g *queue.Goal, res *Result) string {
	if res == nil {
		return "not ready to land yet: P merges it into " + g.Base + ", F opens its pull requests"
	}
	l := res.Landing
	var line string
	switch {
	case l == nil || l.How == "":
		line = "ready to land as " + plural(len(res.Stack), "1 pull request",
			fmt.Sprintf("%d pull requests", len(res.Stack)))
	case l.Merged != "":
		line = "merged into " + l.Upstream(g) + " · " + checksNote(l)
	case l.How == PRs:
		prs := make([]string, 0, len(l.PRs))
		for _, pr := range l.PRs {
			prs = append(prs, fmt.Sprintf("%s %s%s", prName(pr.URL), strings.ToLower(pr.State),
				checkMark(pr.Checks)))
		}
		line = "pull requests " + strings.Join(prs, ", ")
	default:
		line = "pushed, waiting to show up on " + l.Upstream(g)
	}
	if l != nil && l.Error != "" {
		line += " · last check failed: " + l.Error
	}
	return line
}

func checksNote(l *Landing) string {
	switch l.Checks {
	case ChecksFailed:
		return "checks failing: " + strings.Join(l.Failing, ", ")
	case ChecksPending:
		return "checks running"
	case ChecksPassed:
		return "checks pass"
	}
	return "waiting for checks"
}

func checkMark(checks string) string {
	return map[string]string{ChecksPassed: " ✓", ChecksFailed: " ✗", ChecksPending: " …"}[checks]
}

// prName shortens a pull request's URL to its number.
func prName(url string) string {
	if i := strings.LastIndex(url, "/"); i >= 0 && i < len(url)-1 {
		return "#" + url[i+1:]
	}
	return url
}

// FollowLocal brings the repo's own base branch up to what just landed on
// remote, when that can't lose anything: a fast-forward, never over commits
// of the human's own, and where the branch is checked out, never over
// uncommitted changes to the files that landed. Nothing is stashed. It says
// what it did, or why it left the branch for the human to pull.
func FollowLocal(ctx context.Context, s *queue.Store, g *queue.Goal, remote, tip string) string {
	repo := git.Repo{Dir: s.Repo()}
	_, _ = repo.Run(ctx, "fetch", "--quiet", remote, g.Base)
	local, err := repo.RevParse(ctx, g.Base)
	if err != nil {
		return "your " + g.Base + " wasn't moved: " + err.Error()
	}
	if ok, _ := repo.IsAncestor(ctx, local, tip); !ok {
		return "your " + g.Base + " has commits of its own, so pull it when ready"
	}
	if dir := checkedOut(ctx, repo, g.Base); dir != "" {
		if _, err := (git.Repo{Dir: dir}).Run(
			ctx,
			"merge",
			"--ff-only",
			"--quiet",
			tip,
		); err != nil {
			return "your " + g.Base + " wasn't moved, since your uncommitted changes touch what landed: " +
				"pull it when ready"
		}
		return "your " + g.Base + " follows"
	}
	if _, err := repo.Run(ctx, "update-ref", "refs/heads/"+g.Base, tip, local); err != nil {
		return "your " + g.Base + " wasn't moved: " + err.Error()
	}
	return "your " + g.Base + " follows"
}

// checkedOut is the worktree with branch checked out, "" for none.
func checkedOut(ctx context.Context, repo git.Repo, branch string) string {
	out, err := repo.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	dir := ""
	for line := range strings.SplitSeq(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			dir = path
		}
		if line == "branch refs/heads/"+branch {
			return dir
		}
	}
	return ""
}
