package finish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// WatchEvery is how often a done goal's landing is checked upstream.
const WatchEvery = 2 * time.Minute

// NoChecksGrace is how long a goal merged upstream waits for checks to show
// up on its base branch before it counts as finished without any.
const NoChecksGrace = 10 * time.Minute

// How is how a goal was landed.
type How string

// The ways to land a goal.
const (
	// Push pushes the goal straight to its base branch.
	Push How = "push"
	// PRs opens its stack of pull requests.
	PRs How = "prs"
)

// The verdicts of a commit's checks.
const (
	ChecksNone    = "none"
	ChecksPending = "pending"
	ChecksPassed  = "passed"
	ChecksFailed  = "failed"
)

// Landing is how far a done goal is on its way upstream.
type Landing struct {
	Remote string `yaml:"remote"`
	// How is empty until the goal is pushed or its pull requests opened.
	How How `yaml:"how,omitempty"`
	// PRs are the stack's pull requests, as last seen.
	PRs []PRState `yaml:"prs,omitempty"`
	// Merged is the commit of the base branch upstream the goal was first
	// seen merged into, at MergedAt.
	Merged   string    `yaml:"merged,omitempty"`
	MergedAt time.Time `yaml:"mergedAt,omitempty"`
	// Checks is the verdict of the checks on the base branch's tip upstream
	// since the goal merged, and Failing names the checks that failed.
	Checks  string   `yaml:"checks,omitempty"`
	Failing []string `yaml:"failing,omitempty"`
	// Checked is when the landing was last checked, and Error why that
	// check failed.
	Checked time.Time `yaml:"checked,omitempty"`
	Error   string    `yaml:"error,omitempty"`
}

// PRState is one pull request of the stack as last seen.
type PRState struct {
	URL string `yaml:"url"`
	// State is GitHub's: OPEN, MERGED or CLOSED.
	State  string `yaml:"state,omitempty"`
	Checks string `yaml:"checks,omitempty"`
}

// Upstream is the remote-tracking branch the goal lands on.
func (l *Landing) Upstream(g *queue.Goal) string { return l.Remote + "/" + g.Base }

// landing returns res's landing, creating it on origin.
func (r *Result) landing() *Landing {
	if r.Landing == nil {
		r.Landing = &Landing{Remote: "origin"}
	}
	return r.Landing
}

// Ready returns the goal's layout while it still matches the goal's
// branches, or nil when it has to be laid out again.
func Ready(ctx context.Context, s *queue.Store, g *queue.Goal) (*Result, error) {
	res, err := Load(s.GoalDir(g.Name))
	if err != nil || res == nil || !Current(ctx, s, g, res) {
		return nil, err
	}
	return res, nil
}

// Land pushes the laid-out goal straight to its base branch, or opens its
// stack of pull requests, on remote, and records how on the layout so the
// scheduler watches it land. Pushing refuses a goal whose layout fails the
// gate unless force is set. It returns the pull requests' URLs.
func Land(
	ctx context.Context,
	s *queue.Store,
	g *queue.Goal,
	res *Result,
	how How,
	remote string,
	force bool,
	gh GH,
) ([]string, error) {
	var urls []string
	switch how {
	case Push:
		if last := res.Stack[len(res.Stack)-1]; last.Gate != nil && !last.Gate.Passed && !force {
			return nil, fmt.Errorf(
				"goal %s fails the gate on %s: fix it, or push anyway with -force",
				g.Name,
				res.Final,
			)
		}
		if err := push(ctx, s, g, res, remote); err != nil {
			return nil, err
		}
	case PRs:
		var err error
		if urls, err = OpenPRs(ctx, s, g, res, remote, gh); err != nil {
			return urls, err
		}
	default:
		return nil, fmt.Errorf("no way to land called %q", how)
	}
	l := res.landing()
	*l = Landing{Remote: remote, How: how}
	for _, u := range urls {
		l.PRs = append(l.PRs, PRState{URL: u, State: "OPEN"})
	}
	return urls, Save(s.GoalDir(g.Name), res)
}

// Watch checks, at most every WatchEvery, whether a done goal has landed:
// all of its changes are on its base branch upstream, and the checks there
// pass. A landed goal is finished. It reports whether the goal is finished.
// A goal without a layout, or whose remote is missing, is left done.
func Watch(ctx context.Context, s *queue.Store, g *queue.Goal, gh GH, now time.Time) (bool, error) {
	if g.State != queue.GoalDone {
		return g.State == queue.GoalFinished, nil
	}
	res, err := Load(s.GoalDir(g.Name))
	if err != nil || res == nil {
		return false, err
	}
	l := res.landing()
	if !l.Checked.IsZero() && now.Sub(l.Checked) < WatchEvery {
		return false, nil
	}
	l.Checked = now
	finished, err := watch(ctx, git.Repo{Dir: s.Repo()}, g, res, gh, now)
	l.Error = ""
	if err != nil {
		l.Error = err.Error()
	}
	if saveErr := Save(s.GoalDir(g.Name), res); saveErr != nil {
		return false, errors.Join(err, saveErr)
	}
	if !finished {
		return false, err
	}
	g.State = queue.GoalFinished
	return true, s.SaveGoal(g)
}

func watch(
	ctx context.Context,
	repo git.Repo,
	g *queue.Goal,
	res *Result,
	gh GH,
	now time.Time,
) (bool, error) {
	l := res.Landing
	// The URL as configured, before insteadOf rewrites it.
	url, err := repo.Run(ctx, "config", "--get", "remote."+l.Remote+".url")
	switch {
	case err != nil && l.How == "":
		return false, nil // not landed, and nowhere it could have been
	case err != nil:
		return false, fmt.Errorf("there is no remote %s to land on", l.Remote)
	}
	if _, err := repo.Run(ctx, "fetch", "--quiet", l.Remote,
		"+refs/heads/"+g.Base+":refs/remotes/"+l.Upstream(g)); err != nil {
		return false, err
	}
	var errs []error
	for i := range l.PRs {
		if err := prState(ctx, gh, repo.Dir, &l.PRs[i]); err != nil {
			errs = append(errs, err)
		}
	}
	upstream, err := repo.RevParse(ctx, l.Upstream(g))
	if err != nil {
		return false, err
	}
	merged, err := Merged(ctx, repo, res.Tip(), upstream)
	if err != nil || !merged {
		l.Merged, l.MergedAt, l.Checks, l.Failing = "", time.Time{}, "", nil
		return false, errors.Join(append(errs, err)...)
	}
	if l.Merged == "" {
		l.Merged, l.MergedAt = upstream, now
	}
	// Checks are read from GitHub; a remote elsewhere has none to read.
	var runs []check
	if strings.Contains(url, "github.com") {
		if runs, err = checkRuns(ctx, gh, repo.Dir, upstream); err != nil {
			return false, errors.Join(append(errs, err)...)
		}
	}
	l.Checks, l.Failing = verdict(runs)
	finished := l.Checks == ChecksPassed ||
		l.Checks == ChecksNone && now.Sub(l.MergedAt) >= NoChecksGrace
	return finished, errors.Join(errs...)
}

// Merged reports whether upstream holds every change of tip: tip is in its
// history, or merging tip in would change nothing, as after the goal was
// squashed or rebased on the way in.
func Merged(ctx context.Context, repo git.Repo, tip, upstream string) (bool, error) {
	if in, err := repo.IsAncestor(ctx, tip, upstream); err != nil || in {
		return in, err
	}
	tree, conflicted, err := repo.MergeTree(ctx, upstream, tip)
	if err != nil || conflicted {
		return false, err
	}
	have, err := repo.Run(ctx, "rev-parse", upstream+"^{tree}")
	return tree == have, err
}

// check is one check on a commit, GitHub's check run or commit status
// alike.
type check struct {
	name, status, conclusion string
}

// checkRuns lists the check runs on a commit upstream.
func checkRuns(ctx context.Context, gh GH, dir, sha string) ([]check, error) {
	out, err := gh(ctx, dir, "api", "repos/{owner}/{repo}/commits/"+sha+"/check-runs",
		"--jq", `.check_runs[] | [.name, .status, (.conclusion // "")] | @tsv`)
	if err != nil {
		return nil, err
	}
	var runs []check
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Split(line, "\t"); len(f) == 3 {
			runs = append(runs, check{name: f[0], status: f[1], conclusion: f[2]})
		}
	}
	return runs, nil
}

// failed are the conclusions of a check that didn't pass.
var failed = []string{
	"failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale", "error",
}

// verdict sums checks up, and names the ones that failed.
func verdict(checks []check) (string, []string) {
	if len(checks) == 0 {
		return ChecksNone, nil
	}
	var failing []string
	pending := false
	for _, c := range checks {
		switch {
		case !strings.EqualFold(c.status, "completed"):
			pending = true
		case slices.Contains(failed, strings.ToLower(c.conclusion)):
			failing = append(failing, c.name)
		}
	}
	switch {
	case len(failing) > 0:
		return ChecksFailed, failing
	case pending:
		return ChecksPending, nil
	}
	return ChecksPassed, nil
}

// prState refreshes one pull request's state and checks.
func prState(ctx context.Context, gh GH, dir string, pr *PRState) error {
	out, err := gh(ctx, dir, "pr", "view", pr.URL, "--json", "state,statusCheckRollup")
	if err != nil {
		return err
	}
	var v struct {
		State  string `json:"state"`
		Rollup []struct {
			Name       string `json:"name"`
			Context    string `json:"context"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			// State is a commit status's, which has no status.
			State string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return fmt.Errorf("reading %s: %w", pr.URL, err)
	}
	var checks []check
	for _, r := range v.Rollup {
		c := check{name: r.Name + r.Context, status: r.Status, conclusion: r.Conclusion}
		if r.State != "" {
			c.status, c.conclusion = "completed", r.State
			if strings.EqualFold(r.State, "pending") || strings.EqualFold(r.State, "expected") {
				c.status = "pending"
			}
		}
		checks = append(checks, c)
	}
	pr.State = v.State
	pr.Checks, _ = verdict(checks)
	return nil
}

// RemoveWorktrees removes a finished goal's worktrees. A worktree with
// changes in it is kept, since git won't remove it without forcing; the
// error names it.
func RemoveWorktrees(ctx context.Context, s *queue.Store, g *queue.Goal) error {
	dirs, err := filepath.Glob(s.WorktreeDir(g.Name, "*"))
	if err != nil {
		return err
	}
	repo := git.Repo{Dir: s.Repo()}
	var errs []error
	for _, dir := range dirs {
		if _, err := repo.Run(ctx, "worktree", "remove", dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
