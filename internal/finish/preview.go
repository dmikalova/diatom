package finish

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// Mirror force-pushes a goal's integration branch to the branch its pull
// request lives on, so there is always something to open one from (ADR 0014).
// The branch is diatom's own and is rewritten at landing; the lease still
// keeps a commit someone else pushed to it.
func Mirror(ctx context.Context, s *queue.Store, g *queue.Goal, remote string) error {
	repo := git.Repo{Dir: s.Repo()}
	if !repo.BranchExists(ctx, g.IntegrationBranch()) {
		return nil
	}
	_, err := repo.Run(ctx, "push", "--force-with-lease", remote,
		g.IntegrationBranch()+":refs/heads/"+ReviewBranch(g))
	return err
}

// FindPR is the goal's open pull request, "" when it has none. The branch
// is its identity, so finding it costs one read and no push (ADR 0014).
func FindPR(ctx context.Context, dir string, g *queue.Goal, gh GH) string {
	url, err := gh(ctx, dir, "pr", "view", ReviewBranch(g), "--json", "url", "--jq", ".url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(url)
}

// ErrNoCommits is what EnsurePR answers when the goal has nothing to open a
// pull request with. It is the agent's to fix, not the human's: the task
// runs again and commits first.
var ErrNoCommits = errors.New("the goal has no commits of its own yet")

// EnsurePR returns the goal's open pull request, opening it as a draft when
// it has none. The branch is the only identity the pull request needs, so
// nothing about it is stored (ADR 0014).
func EnsurePR(
	ctx context.Context,
	s *queue.Store,
	g *queue.Goal,
	remote string,
	gh GH,
) (string, error) {
	repo := git.Repo{Dir: s.Repo()}
	if ReviewBranch(g) == g.Base {
		return "", fmt.Errorf("goal %s has %s as both its branch and its base, so it has "+
			"nowhere to open a pull request: give it a branch of its own in goal.yaml",
			g.Name, g.Base)
	}
	ahead, err := commitsOfItsOwn(ctx, repo, g)
	if err != nil {
		return "", err
	}
	if !ahead {
		return "", ErrNoCommits
	}
	if err := Mirror(ctx, s, g, remote); err != nil {
		return "", err
	}
	branch := ReviewBranch(g)
	dir := s.Repo()
	if url := FindPR(ctx, dir, g, gh); url != "" {
		return url, nil
	}
	if !repo.RemoteHasBranch(ctx, remote, g.Base) {
		// GitHub answers this as an unreadable GraphQL error, so it is asked
		// here instead.
		return "", fmt.Errorf("%s has no branch %s, which goal %s is based on: push it, and "+
			"the pull request opens the next time a task asks for its checks",
			remote, g.Base, g.Name)
	}
	out, err := gh(ctx, dir, "pr", "create", "--draft",
		"--head", branch, "--base", g.Base, "--title", g.Title, "--body", draftBody(g))
	if err != nil {
		return "", err
	}
	lines := strings.Split(out, "\n")
	return lines[len(lines)-1], nil
}

// commitsOfItsOwn reports whether the goal's branch is ahead of its base.
func commitsOfItsOwn(ctx context.Context, repo git.Repo, g *queue.Goal) (bool, error) {
	if !repo.BranchExists(ctx, g.IntegrationBranch()) {
		return false, nil
	}
	ahead, err := repo.Run(ctx, "rev-list", "--count", g.Base+".."+g.IntegrationBranch())
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(ahead) != "0", nil
}

func draftBody(g *queue.Goal) string {
	return fmt.Sprintf(
		"Goal %s, open while diatom works on it. It is a draft until the goal is done, and "+
			"diatom replaces its commits with the laid-out ones when the goal lands. Only a "+
			"human merges it.\n",
		g.Name,
	)
}

// PRChecks reads the verdict of the checks on a pull request's head commit,
// with the names of the ones that failed.
func PRChecks(ctx context.Context, dir, url string, gh GH) (string, []string, error) {
	pr := PRState{URL: url}
	if err := prState(ctx, gh, dir, &pr); err != nil {
		return "", nil, err
	}
	return pr.Checks, pr.Failing, nil
}

// PRHead is the commit a pull request's checks are running on.
func PRHead(ctx context.Context, dir, url string, gh GH) (string, error) {
	return gh(ctx, dir, "pr", "view", url, "--json", "headRefOid", "--jq", ".headRefOid")
}

// Label puts labels on a pull request, or takes them off, and returns the
// ones it changed. A repo's workflows may key off a label, so a CI ask can
// carry them (ADR 0014); only diatom may write them.
func Label(ctx context.Context, dir, url string, gh GH, labels []string, on bool) []string {
	flag := "--remove-label"
	if on {
		flag = "--add-label"
	}
	var done []string
	for _, name := range labels {
		if _, err := gh(ctx, dir, "pr", "edit", url, flag, name); err == nil {
			done = append(done, name)
		}
	}
	return done
}

// CheckOutput is what the failing checks printed, as far as gh will say
// without the run logs, which need a workflow run rather than a check.
func CheckOutput(ctx context.Context, dir, sha string, gh GH, failing []string) string {
	out, err := gh(ctx, dir, "api", "repos/{owner}/{repo}/commits/"+sha+"/check-runs",
		"--jq", `.check_runs[] | [.name, .html_url, (.output.title // ""), `+
			`(.output.summary // "")] | @tsv`)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || !slices.ContainsFunc(failing, func(n string) bool {
			return strings.EqualFold(n, f[0])
		}) {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", f[0], f[1])
		if f[2] != "" || f[3] != "" {
			fmt.Fprintf(&b, "%s\n\n%s\n\n", f[2], strings.ReplaceAll(f[3], `\n`, "\n"))
		}
	}
	return b.String()
}
