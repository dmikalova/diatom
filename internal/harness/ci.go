package harness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// ciRounds is how many verdicts one task may have before a failing check
// becomes a question. An agent that reads the same failure twice judges a
// flake better than a heuristic could, but it must not spin on one at model
// prices (ADR 0014).
const ciRounds = 3

// ciEvery is how often a task waiting on a commit's checks is looked up.
const ciEvery = 90 * time.Second

// applyCI parks the tasks that asked for the goal's checks. Nothing about
// the pull request is stored: the branch is its identity, so the commit the
// task waits for is the whole record (ADR 0014).
func (h *Harness) applyCI(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	report session.Report,
) error {
	if len(report.CI) == 0 {
		return nil
	}
	url, head, err := h.openPR(ctx, repo, g)
	for _, e := range report.CI {
		t, loadErr := repo.Store.Task(g.Name, e.Task)
		if loadErr != nil {
			return loadErr
		}
		if err != nil {
			if askErr := h.askAbout(repo.Store, g, t, "Pushing the goal's branch failed",
				err.Error()); askErr != nil {
				return askErr
			}
			continue
		}
		t.CI = head
		t.CILabels = finish.Label(ctx, repo.Store.Repo(), url, h.gh(), e.Labels, true)
		if err := repo.Store.SaveTask(g.Name, t); err != nil {
			return err
		}
		if err := repo.Store.AppendNote(g.Name, t.ID, "Waiting for CI",
			waitNote(e, url, head)); err != nil {
			return err
		}
		h.log().Info("task waiting for checks", "goal", g.Name, "task", t.ID, "pr", url)
	}
	return nil
}

func waitNote(e session.Entry, url, head string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nDiatom pushed %s and is waiting for the checks on %s.\n",
		e.Text, url, head[:min(len(head), 12)])
	if len(e.Labels) > 0 {
		fmt.Fprintf(&b, "\nIt put %s on the pull request after the push. A workflow that only "+
			"reads its labels when a run starts may have started without them; if the checks "+
			"come back without the jobs the label turns on, ask again and they will be there "+
			"from the start.\n", strings.Join(e.Labels, ", "))
	}
	return b.String()
}

// openPR mirrors the goal's branch, opens its pull request if it has none,
// and returns the pull request and the commit its checks will run on.
func (h *Harness) openPR(ctx context.Context, repo Repo, g *queue.Goal) (string, string, error) {
	gh := h.gh()
	url, err := finish.EnsurePR(ctx, repo.Store, g, "origin", gh)
	if err != nil {
		return "", "", err
	}
	head, err := finish.PRHead(ctx, repo.Store.Repo(), url, gh)
	return url, head, err
}

// watchCI brings the verdict back to every task of the repo waiting on a
// commit's checks, and releases it.
func (h *Harness) watchCI(ctx context.Context, repo Repo, goals []*queue.Goal) {
	for _, g := range goals {
		if g.State != queue.GoalActive {
			continue
		}
		tasks, err := repo.Store.Tasks(g.Name)
		if err != nil {
			h.log().Warn("reading a goal's tasks failed", "goal", g.Name, "err", err)
			continue
		}
		for _, t := range tasks {
			if t.CI == "" {
				continue
			}
			if err := h.checkTask(ctx, repo, g, t); err != nil {
				h.log().Warn("reading a task's checks failed",
					"goal", g.Name, "task", t.ID, "err", err)
			}
		}
	}
}

// checkTask reads the checks the task waits for, and when they have settled
// writes the verdict onto it and lets it be scheduled again.
func (h *Harness) checkTask(ctx context.Context, repo Repo, g *queue.Goal, t *queue.Task) error {
	if h.recentlyChecked(repo.Store.Repo() + "\x00" + t.CI) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, watchTimeout)
	defer cancel()
	gh, dir := h.gh(), repo.Store.Repo()
	url, err := finish.EnsurePR(ctx, repo.Store, g, "origin", gh)
	if err != nil {
		return err
	}
	verdict, failing, err := finish.PRChecks(ctx, dir, url, gh)
	if err != nil {
		return err
	}
	if verdict == finish.ChecksPending {
		return nil
	}
	sha := t.CI
	t.CI = ""
	t.CIRounds++
	finish.Label(ctx, dir, url, gh, t.CILabels, false)
	t.CILabels = nil
	body := ciNote(ctx, dir, sha, url, verdict, failing, gh)
	if verdict == finish.ChecksFailed && t.CIRounds >= ciRounds {
		if err := repo.Store.SaveTask(g.Name, t); err != nil {
			return err
		}
		return h.askAbout(repo.Store, g, t,
			fmt.Sprintf("The checks on %s keep failing", url), body)
	}
	if err := repo.Store.SaveTask(g.Name, t); err != nil {
		return err
	}
	h.log().Info("checks settled", "goal", g.Name, "task", t.ID, "verdict", verdict)
	return repo.Store.AppendNote(g.Name, t.ID, "CI", body)
}

func ciNote(
	ctx context.Context,
	dir, sha, url, verdict string,
	failing []string,
	gh finish.GH,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The checks on %s, commit %s: **%s**.\n", url, sha[:min(len(sha), 12)], verdict)
	switch verdict {
	case finish.ChecksNone:
		b.WriteString("\nNothing ran. The pull request may have no workflow that applies to it.\n")
	case finish.ChecksFailed:
		fmt.Fprintf(&b, "\nFailing: %s\n\n", strings.Join(failing, ", "))
		b.WriteString(finish.CheckOutput(ctx, dir, sha, gh, failing))
		b.WriteString("\nFix what failed. If the same check failed the same way last round and " +
			"nothing you changed could explain it, it is flaky: say so in a note and run " +
			"`diatom task ci` again rather than changing more code.\n")
	}
	return b.String()
}

// askAbout parks a task on a question for the human, for the failures no
// agent can clear.
func (h *Harness) askAbout(s *queue.Store, g *queue.Goal, t *queue.Task, title, body string) error {
	q := &queue.Question{
		Task:    t.ID,
		Created: h.now(),
		Text:    "## " + title + "\n\n" + body,
	}
	return s.AddQuestion(g.Name, q)
}

func (h *Harness) gh() finish.GH {
	if h.GH != nil {
		return h.GH
	}
	return finish.RunGH
}

// mirror pushes the goal's integration branch to the branch its pull request
// lives on. A repo with no remote, or none reachable, only logs: the push
// becomes a question when a task actually asks for the checks.
func (h *Harness) mirror(ctx context.Context, repo Repo, g *queue.Goal) {
	unlock := h.lockRepo(repo.Store.Repo())
	defer unlock()
	if err := finish.Mirror(ctx, repo.Store, g, "origin"); err != nil {
		h.log().Debug("mirroring a goal's branch failed", "goal", g.Name, "err", err)
	}
}

// recentlyChecked reports whether a commit's checks were read within
// ciEvery, and records this read otherwise. The record is in memory only:
// losing it on a restart costs one extra call to GitHub.
func (h *Harness) recentlyChecked(key string) bool {
	now := h.now()
	if v, ok := h.checked.Load(key); ok {
		if last, ok := v.(time.Time); ok && now.Sub(last) < ciEvery {
			return true
		}
	}
	h.checked.Store(key, now)
	return false
}
