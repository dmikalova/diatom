package harness

import (
	"fmt"
	"strings"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/finish"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/state"
)

// problemTail is how much of a problem's output goes in the task the reply
// makes. The whole of it stays in the problem file.
const problemTail = 60

// hit files a problem on a goal. Filing one must never stop what filed it,
// so a failure here only reaches the log.
func (h *Harness) hit(s *queue.Store, goal string, p *queue.Problem) {
	p.Created = h.now()
	if err := s.HitProblem(goal, p); err != nil {
		h.log().Error("recording a problem failed", "goal", goal, "what", p.What, "err", err)
	}
}

// applyReplies turns every open problem the human has replied to into a task
// of the goal's, and closes it. The reply is the instruction and the failure
// is the evidence, the way a rejected hunk becomes a revision.
func (h *Harness) applyReplies(s *queue.Store, g *queue.Goal) error {
	open, err := s.Problems(g.Name, queue.ProblemOpen)
	if err != nil {
		return err
	}
	for _, p := range open {
		if p.Fix != "" {
			done, err := h.applyFix(s, g, p)
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		if p.Reply == "" {
			continue
		}
		ws, err := problemWorkstream(s, g)
		if err != nil {
			return err
		}
		t := &queue.Task{
			Title:      "Fix: " + p.What,
			Kind:       queue.Revision,
			Workstream: ws,
			Origin:     queue.Origin{Type: "problem", Ref: p.ID},
			Created:    h.now(),
			Body:       problemBody(p),
		}
		if err := s.AddTask(g.Name, t); err != nil {
			return err
		}
		if err := s.CloseProblem(g.Name, p); err != nil {
			return err
		}
	}
	return nil
}

// applyFix carries out the fix a problem's kind offered and the human took.
// It reports whether the problem is dealt with, and so needs no task.
func (h *Harness) applyFix(s *queue.Store, g *queue.Goal, p *queue.Problem) (bool, error) {
	if p.Kind != queue.ProblemPRClosed {
		return false, nil
	}
	var err error
	switch p.Fix {
	case queue.ReopenFix:
		err = finish.Reopen(s, g)
	default:
		err = finish.Replace(s, g, p.Fix)
	}
	if err != nil {
		return false, err
	}
	return true, s.CloseProblem(g.Name, p)
}

// watchClosed files a problem when a pull request of a done goal was closed
// without being merged. The goal can go no further by itself: only the human
// knows whether the work moved to another pull request, wants a fresh one,
// or is to be given up (ADR 0014).
func (h *Harness) watchClosed(s *queue.Store, g *queue.Goal) {
	res, err := finish.Load(s.GoalDir(g.Name))
	if err != nil || res == nil || res.Landing == nil || res.Landing.Merged != "" {
		return
	}
	var closed []string
	for _, pr := range res.Landing.PRs {
		if pr.State == "CLOSED" {
			closed = append(closed, pr.URL)
		}
	}
	if len(closed) == 0 {
		return
	}
	h.hit(s, g.Name, &queue.Problem{
		Kind: queue.ProblemPRClosed,
		What: "A pull request of " + g.Name + " was closed without being merged",
		Op:   "land",
		Text: "Closed without merging:\n" + strings.Join(closed, "\n"),
	})
}

// orphans files a problem for every queue whose repository is gone from
// where its marker says it is. They go on the repo's intake goal, which is
// where anything that belongs to no goal of the human's lives. Diatom asks
// rather than guess: a repo moved on disk is not a repo deleted (ADR 0013).
func (h *Harness) orphans(s *queue.Store) {
	if len(h.Stores) == 0 {
		return
	}
	for dir, repo := range state.Orphans(config.Paths{Home: h.Paths.Home}.StateDir()) {
		h.hit(s, queue.IntakeGoal, &queue.Problem{
			Kind: queue.ProblemWorktree,
			What: "The repo of " + dir + " is gone from " + repo,
			Op:   "open " + dir,
			Text: "Diatom keeps the goals of " + dir + " and the worktrees they work in.\n" +
				"Its repo was at " + repo + ", and nothing is there now.\n\n" +
				"Say where it moved to, or delete what is kept of it.",
		})
	}
}

// problemWorkstream is the workstream a problem's fix belongs in: the one
// the goal's last unfinished work is in, or its first.
func problemWorkstream(s *queue.Store, g *queue.Goal) (string, error) {
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return "", err
	}
	var last string
	for _, t := range tasks {
		if t.Workstream == "" {
			continue
		}
		if last == "" || t.State != queue.Done {
			last = t.Workstream
		}
	}
	return last, nil
}

// problemBody is what the agent reads: what the human said to do, then what
// failed and the tail of its output.
func problemBody(p *queue.Problem) string {
	var b strings.Builder
	b.WriteString(p.Reply)
	b.WriteString("\n\n## What failed\n\n")
	fmt.Fprintf(&b, "%s, doing %s.", p.What, p.Op)
	if p.Count > 1 {
		fmt.Fprintf(&b, " It has failed %d times.", p.Count)
	}
	if p.Fix != "" {
		fmt.Fprintf(&b, "\n\nThe human gave: %s", p.Fix)
	}
	if out := strings.TrimSpace(p.Text); out != "" {
		b.WriteString("\n\n```\n")
		b.WriteString(tail(out, problemTail))
		b.WriteString("\n```\n")
	}
	return b.String()
}

// tail is the last n lines of s, marked when there were more.
func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("… %d earlier lines\n%s",
		len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}
