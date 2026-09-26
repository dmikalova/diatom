package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

// planningWorktree is the worktree triage and grilling read the code in: the
// goal's integration branch, detached. Its name can't be a workstream's.
const planningWorktree = "_planning"

// planningKind reports whether a task kind runs as a planning session: no
// workstream, no gate and no commit, only what it hands in through the task
// tool.
func planningKind(k queue.Kind) bool { return k == queue.Triage || k == queue.Grilling }

// applyRepoIntake turns intake aimed at the repo rather than a goal into new
// goals: with only a repo focused, what the human types is a new goal, which
// starts in planning with a grilling task (ADR 0010).
func (h *Harness) applyRepoIntake(ctx context.Context, s *queue.Store) error {
	items, err := intake.Pending(intake.Dir(s.Repo(), ""))
	if err != nil {
		return err
	}
	for _, in := range items {
		title, _, _ := strings.Cut(in.Text, "\n")
		g, err := plan.NewGoal(
			ctx,
			s,
			"",
			title,
			in.Text,
			queue.Origin{Type: "intake", Ref: filepath.Base(in.Path)},
			h.now(),
		)
		if err != nil {
			return err
		}
		h.log().Info("new goal from intake", "repo", s.Repo(), "goal", g.Name)
		if err := intake.Done(in); err != nil {
			return err
		}
	}
	return nil
}

// applyGoalIntake gives each intake of an active goal a triage task (ADR
// 0009). The intake stays until its triage is done.
func (h *Harness) applyGoalIntake(s *queue.Store, goal string, tasks []*queue.Task) error {
	items, err := intake.Pending(intake.Dir(s.Repo(), goal))
	if err != nil {
		return err
	}
	for _, in := range items {
		ref := filepath.Base(in.Path)
		if slices.ContainsFunc(tasks, func(t *queue.Task) bool {
			return t.Kind == queue.Triage && t.Origin.Type == "intake" && t.Origin.Ref == ref
		}) {
			continue
		}
		first, _, _ := strings.Cut(in.Text, "\n")
		if len(first) > 60 {
			first = first[:60] + "…"
		}
		if err := s.AddTask(goal, &queue.Task{
			Title:   "Triage: " + first,
			Kind:    queue.Triage,
			Origin:  queue.Origin{Type: "intake", Ref: ref},
			Created: h.now(),
			Body:    in.Text,
		}); err != nil {
			return err
		}
	}
	return nil
}

// applyPlanningIntake passes intake for a goal still in planning to its
// grilling as feedback. A plan handed in and not yet signed off is set aside,
// and grilling starts another round with the feedback.
func (h *Harness) applyPlanningIntake(s *queue.Store, goal string, tasks []*queue.Task) error {
	items, err := intake.Pending(intake.Dir(s.Repo(), goal))
	if err != nil || len(items) == 0 {
		return err
	}
	var grill *queue.Task
	for _, t := range tasks {
		if t.Kind == queue.Grilling {
			grill = t
		}
	}
	if grill == nil || grill.State == queue.Active {
		return nil // an active round gets the feedback once it ends
	}
	for _, in := range items {
		grill.Body = appendSection(grill.Body, "Feedback from the human", in.Text)
	}
	if grill.State == queue.Done {
		if err := plan.Supersede(s.GoalDir(goal), h.now()); err != nil {
			return err
		}
		grill.Attempts = 0
		if err := s.Move(goal, grill, queue.Pending); err != nil {
			return err
		}
	} else if err := s.SaveTask(goal, grill); err != nil {
		return err
	}
	for _, in := range items {
		if err := intake.Done(in); err != nil {
			return err
		}
	}
	return nil
}

// runPlanning runs a triage or grilling batch in the planning worktree.
func (h *Harness) runPlanning(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
) error {
	s := repo.Store
	main := git.Repo{Dir: s.Repo()}
	wt := git.Repo{Dir: s.WorktreeDir(g.Name, planningWorktree)}
	unlock := h.lockRepo(s.Repo())
	err := main.CreateBranch(ctx, g.IntegrationBranch(), g.Base)
	if err == nil {
		err = main.EnsureDetached(ctx, wt.Dir, g.IntegrationBranch())
	}
	unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(plan.DraftsDir(s.GoalDir(g.Name)), 0o755); err != nil {
		return err
	}
	for _, t := range b.Tasks {
		if err := s.Move(g.Name, t, queue.Active); err != nil {
			return err
		}
	}
	dir, spec, err := h.newSession(ctx, repo, g, b, wt)
	if err != nil {
		return h.requeue(s, g.Name, b.Tasks, nil, err)
	}
	res, runErr := h.runSession(ctx, repo, g, b, wt, dir, spec)
	if err := session.WriteResult(
		dir,
		sessionResult{Result: res, Error: errString(runErr)},
	); err != nil {
		h.log().Warn("writing the session result failed", "session", spec.ID, "err", err)
	}
	return h.finishPlanning(ctx, repo, g, b, dir, res, runErr)
}

// finishPlanning applies what a triage or grilling session handed in.
func (h *Harness) finishPlanning(
	ctx context.Context,
	repo Repo,
	g *queue.Goal,
	b schedule.Batch,
	dir string,
	res runner.Result,
	runErr error,
) error {
	s := repo.Store
	report, err := session.ReadReport(dir)
	if err != nil {
		return err
	}
	for _, n := range report.Notes {
		if err := s.AppendNote(g.Name, n.Task, "Note", n.Text); err != nil {
			return err
		}
	}
	asked := map[string]bool{}
	ask := func(task, text string) error {
		asked[task] = true
		return s.AddQuestion(g.Name, &queue.Question{Task: task, Text: text, Created: h.now()})
	}
	for _, q := range report.Questions {
		if err := ask(q.Task, q.Text); err != nil {
			return err
		}
	}
	planned := map[string]bool{}
	if runErr == nil {
		if err := h.applyAdds(ctx, repo, g, report, ask); err != nil {
			return err
		}
		if planned, err = h.applyPlan(repo, g, report); err != nil {
			return err
		}
	}
	// Reread the tasks after everything above may have noted on them.
	tasks, err := h.reload(s, g.Name, b.Tasks)
	if err != nil {
		return err
	}
	if runErr != nil {
		return h.requeue(s, g.Name, tasks, asked, runErr)
	}

	share := usageShare(filepath.Base(dir), res.Usage, len(tasks))
	for _, t := range tasks {
		t.Usage = append(t.Usage, share)
		done := report.Done[t.ID]
		if t.Kind == queue.Grilling && done && !planned[t.ID] && !asked[t.ID] {
			t.Body = appendSection(
				t.Body,
				"No plan",
				"The session marked grilling done without handing in "+
					"a plan with `diatom task plan`.",
			)
			done = false
		}
		if err := h.settle(repo, g.Name, t, done, asked[t.ID]); err != nil {
			return err
		}
		// A triage that asked is waiting on the human, not done.
		if done && !asked[t.ID] && t.Kind == queue.Triage && t.Origin.Type == "intake" {
			if err := doneIntake(s.Repo(), g.Name, t.Origin.Ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyAdds queues the tasks and goals triage handed in. A task on a
// workstream the goal doesn't have, or after a task that doesn't exist, goes
// to the human instead: adding a workstream or changing dependencies needs
// their approval (ADR 0010).
func (h *Harness) applyAdds(ctx context.Context, repo Repo, g *queue.Goal, report session.Report,
	ask func(task, text string) error,
) error {
	s := repo.Store
	existing, err := s.Tasks(g.Name)
	if err != nil {
		return err
	}
	for _, e := range report.Adds {
		var problems []string
		if _, ok := g.Workstream(e.Workstream); !ok {
			problems = append(problems, fmt.Sprintf("the goal has no workstream %q", e.Workstream))
		}
		for _, id := range e.After {
			if !slices.ContainsFunc(existing, func(t *queue.Task) bool { return t.ID == id }) {
				problems = append(problems, "there is no task "+id+" to come after")
			}
		}
		if _, ok := repo.Config.Profiles[e.Profile]; e.Profile != "" && !ok {
			problems = append(problems, fmt.Sprintf("there is no profile %q", e.Profile))
		}
		if len(problems) > 0 {
			if err := ask(
				e.Task,
				fmt.Sprintf("Triage wanted to add the task %q on workstream %s, but %s. "+
					"Should it be added, and where?\n\n%s", e.Title, e.Workstream, strings.Join(problems, " and "),
					e.Text),
			); err != nil {
				return err
			}
			continue
		}
		t := &queue.Task{
			Title:      e.Title,
			Kind:       queue.Planned,
			Profile:    e.Profile,
			Workstream: e.Workstream,
			DependsOn:  e.After,
			Origin:     queue.Origin{Type: "triage", Ref: e.Task},
			Created:    h.now(),
			Body:       e.Text,
		}
		if err := s.AddTask(g.Name, t); err != nil {
			return err
		}
		existing = append(existing, t)
	}
	for _, e := range report.Goals {
		body := e.Text
		if body == "" {
			body = e.Title
		}
		ng, err := plan.NewGoal(
			ctx,
			s,
			"",
			e.Title,
			body,
			queue.Origin{Type: "triage", Ref: g.Name + "/" + e.Task},
			h.now(),
		)
		if err != nil {
			return err
		}
		h.log().Info("new goal from triage", "repo", s.Repo(), "goal", ng.Name)
	}
	return nil
}

// applyPlan saves the plan grilling handed in, and reports which grilling
// tasks handed in one that holds up against the config. A plan that doesn't
// is noted on its task, which then runs again.
func (h *Harness) applyPlan(
	repo Repo,
	g *queue.Goal,
	report session.Report,
) (map[string]bool, error) {
	planned := map[string]bool{}
	if len(report.Plans) == 0 {
		return planned, nil
	}
	e := report.Plans[len(report.Plans)-1]
	p, err := plan.Parse([]byte(e.Text))
	if err == nil {
		err = p.Validate(repo.Config.Profiles)
	}
	if err != nil {
		return planned, repo.Store.AppendNote(
			g.Name,
			e.Task,
			"The plan was not accepted",
			err.Error(),
		)
	}
	if err := plan.Save(repo.Store.GoalDir(g.Name), p); err != nil {
		return planned, err
	}
	planned[e.Task] = true
	h.log().Info("plan ready for sign-off", "repo", repo.Store.Repo(), "goal", g.Name)
	return planned, nil
}

// doneIntake moves a triaged intake to done/.
func doneIntake(repo, goal, name string) error {
	in, err := intake.Read(filepath.Join(intake.Dir(repo, goal), name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return intake.Done(in)
}
