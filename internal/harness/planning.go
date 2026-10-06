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
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/runner"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

// planningWorktree is the worktree triage and grilling read the code in. Its
// name can't be a workstream's.
const planningWorktree = "_planning"

// planningKind reports whether a task kind runs as a planning session: no
// workstream, no gate and no commit, only what it hands in through the task
// tool.
func planningKind(k queue.Kind) bool { return k == queue.Triage || k == queue.Grilling }

// applyIntake gives each intake a triage task (ADR 0009). Everything the
// human sends goes to triage, which sorts it into the repo's goals; the goal
// the human was looking at is only a hint. The intake stays until its triage
// is done.
func (h *Harness) applyIntake(ctx context.Context, s *queue.Store) error {
	items, err := intake.Pending(intake.Dir(s.Repo()))
	if err != nil || len(items) == 0 {
		return err
	}
	if err := h.ensureIntakeGoal(ctx, s); err != nil {
		return err
	}
	tasks, err := s.Tasks(queue.IntakeGoal)
	if err != nil {
		return err
	}
	for _, in := range items {
		ref := filepath.Base(in.Path)
		if slices.ContainsFunc(tasks, func(t *queue.Task) bool {
			return t.Kind == queue.Triage && t.Origin.Ref == ref
		}) {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimSpace(in.Text), "\n")
		if len(first) > 60 {
			first = first[:60] + "…"
		}
		if err := s.AddTask(queue.IntakeGoal, &queue.Task{
			Title:   "Triage: " + first,
			Kind:    queue.Triage,
			Origin:  queue.Origin{Type: "intake", Ref: ref},
			Created: h.now(),
			Body:    triageBody(s, in),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ensureIntakeGoal makes the goal that holds the triage tasks. Triage reads
// the base branch as it is, so it follows what is checked out now.
func (h *Harness) ensureIntakeGoal(ctx context.Context, s *queue.Store) error {
	if _, err := s.Goal(queue.IntakeGoal); err == nil {
		return nil
	}
	base, err := git.Repo{Dir: s.Repo()}.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("finding the branch triage reads: %w", err)
	}
	return s.CreateGoal(&queue.Goal{
		Name:    queue.IntakeGoal,
		Title:   "Intake",
		State:   queue.GoalActive,
		Base:    base,
		Created: h.now(),
	})
}

// triageBody is an intake as its triage task reads it: the human's text,
// then where they were when they sent it.
func triageBody(s *queue.Store, in intake.Intake) string {
	body := strings.TrimSpace(in.Text) + "\n\n---\n\n"
	switch g, err := s.Goal(in.Goal); {
	case in.Goal == "":
		body += "Sent with no goal in view."
	case err != nil:
		body += fmt.Sprintf("Sent while looking at goal %s, which no longer exists.", in.Goal)
	default:
		body += fmt.Sprintf("Sent while looking at goal %s (%q, %s): a hint to where it belongs.",
			g.Name, g.Title, g.State)
	}
	if in.Source == "review" {
		body += fmt.Sprintf(" It is a comment the human left while approving hunk %s of commit %s.",
			in.Hunk, in.Commit)
	}
	if c := strings.TrimSpace(in.Context); c != "" {
		body += "\n\nOn their screen when they sent it, which it may or may not be about: " + c
	}
	return body + "\n"
}

// feedbackDir holds the feedback passed to a goal in grilling, until its
// next round.
func feedbackDir(s *queue.Store, goal string) string {
	return plan.FeedbackDir(s.GoalDir(goal))
}

// applyFeedback passes feedback to a goal's grilling. A plan handed in and
// not yet signed off is set aside, and grilling starts another round with the
// feedback. A round already running gets it once it ends.
func (h *Harness) applyFeedback(s *queue.Store, goal string, tasks []*queue.Task) error {
	items, err := intake.Pending(feedbackDir(s, goal))
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
		return nil
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

// runPlanning runs a triage or grilling batch in the planning worktree:
// grilling reads the goal's integration branch, and triage the base branch
// as it is now.
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
	var err error
	if g.Name == queue.IntakeGoal {
		err = main.EnsureDetached(ctx, wt.Dir, goalStart(ctx, main, g))
	} else if err = main.CreateBranch(ctx, g.IntegrationBranch(), goalStart(ctx, main, g)); err == nil {
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
	resume, prompt := h.continueRound(s, g.Name, spec.ID, b)
	if resume != "" {
		h.log().
			Info("continuing the last round's agent session", "session", spec.ID, "goal", g.Name,
				"agentSession", resume)
	}
	return h.runAgent(ctx, repo, g, b, wt, dir, spec, resume, prompt)
}

// roundCache is how long after a planning session ends its agent session is
// carried on rather than started again: within the hour its prompt cache
// lasts, a few minutes short of it (ADR 0011).
const roundCache = 55 * time.Minute

// continueRound is the agent session to carry on for a planning task's next
// round, with the message that carries it on: the last session that worked
// on the task alone, when it ended cleanly within roundCache. Its
// conversation holds what it read and asked, and its cache is still warm, so
// carrying it on reads that back for a tenth of what a new session spends
// finding it all again. Past the hour, the cache is gone and a new session
// costs less.
func (h *Harness) continueRound(
	s *queue.Store,
	goal, current string,
	b schedule.Batch,
) (string, string) {
	if len(b.Tasks) != 1 {
		return "", ""
	}
	t := b.Tasks[0]
	dirs, _ := filepath.Glob(filepath.Join(s.SessionsDir(goal), "*"))
	slices.Sort(dirs)
	for _, dir := range slices.Backward(dirs) {
		spec, err := session.Load(dir)
		if err != nil || spec.ID == current || !slices.Contains(spec.Tasks, t.ID) {
			continue
		}
		at, ended := session.Ended(dir)
		st, err := session.LoadState(dir)
		var res runner.Result
		ok, rerr := session.ReadResult(dir, &res)
		if !ended || err != nil || rerr != nil || !ok || !st.Settled || st.AgentSession == "" ||
			len(
				spec.Tasks,
			) != 1 || res.Outcome != runner.Completed || h.now().Sub(at) > roundCache {
			return "", ""
		}
		return st.AgentSession, fmt.Sprintf(roundNote, t.ID, t.Title, strings.TrimSpace(t.Body))
	}
	return "", ""
}

// roundNote carries a planning session on into its task's next round.
const roundNote = `diatom has picked this session up again for task %s (%q): what the human
answered, or sent back, is now in the task's text, below, after what you read before. Carry on from
where you left off, with what you already know: the next round of questions, the plan, or the triage,
reported through the task tool as before. Your working directory is as you left it, up to date with
any work merged since.

%s`

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
	if err := appendSummaries(s, g.Name, report); err != nil {
		return err
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
		origin := queue.Origin{Type: "triage"}
		if b.Kind != queue.Triage {
			origin = queue.Origin{Type: "goal", Ref: g.Name}
		}
		if err := h.applyTriage(ctx, repo, report, origin, ask); err != nil {
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
		if err := h.settle(repo, g.Name, t, done, asked[t.ID],
			ended(filepath.Base(dir), res)); err != nil {
			return err
		}
		// A triage that asked is waiting on the human, not done.
		if done && !asked[t.ID] && t.Kind == queue.Triage && t.Origin.Type == "intake" {
			if err := doneIntake(s.Repo(), t.Origin.Ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyTriage applies what triage handed in: tasks for the repo's goals,
// feedback for goals in grilling, and new goals.
func (h *Harness) applyTriage(
	ctx context.Context,
	repo Repo,
	report session.Report,
	origin queue.Origin,
	ask func(task, text string) error,
) error {
	s := repo.Store
	for _, e := range report.Feedback {
		if err := h.feedback(s, e.Task, e.Goal, e.Text, ask); err != nil {
			return err
		}
	}
	for _, e := range report.Adds {
		if err := h.applyAdd(repo, e, ask); err != nil {
			return err
		}
	}
	return h.startGoals(ctx, repo, report.Goals, report.Afters, origin, ask)
}

// startGoals starts the goals a session handed in, then makes goals wait
// as it said: goals first, so one can wait for another started beside it.
// Triage starts goals from intake; any other session only when the human
// asked it to, such as in answering its question. A goal whose title is
// taken already, as when a session that handed it in runs again, isn't
// started twice.
func (h *Harness) startGoals(
	ctx context.Context,
	repo Repo,
	goals, afters []session.Entry,
	origin queue.Origin,
	ask func(task, text string) error,
) error {
	existing, err := repo.Store.Goals()
	if err != nil {
		return err
	}
	started := map[string]string{}
	var waits []session.Entry
	for _, e := range goals {
		if i := slices.IndexFunc(existing, func(g *queue.Goal) bool {
			return strings.EqualFold(g.Title, e.Title)
		}); i >= 0 {
			started[strings.ToLower(e.Title)] = existing[i].Name
			continue
		}
		o := origin
		if o.Ref == "" {
			o.Ref = e.Task
		}
		g, err := h.startGoal(ctx, repo, e, o)
		if err != nil {
			return err
		}
		started[strings.ToLower(e.Title)] = g.Name
		if len(e.After) > 0 {
			waits = append(waits, session.Entry{Task: e.Task, Goal: g.Name, After: e.After})
		}
	}
	for _, e := range append(waits, afters...) {
		if err := h.setAfter(repo.Store, e, started, ask); err != nil {
			return err
		}
	}
	return nil
}

// setAfter makes a goal wait for others, each named by its name or title,
// or asks the human when that can't be done.
func (h *Harness) setAfter(
	s *queue.Store,
	e session.Entry,
	started map[string]string,
	ask func(task, text string) error,
) error {
	goals, err := s.Goals()
	if err != nil {
		return err
	}
	resolve := func(ref string) string {
		if name, ok := started[strings.ToLower(ref)]; ok {
			return name
		}
		for _, g := range goals {
			if g.Name == ref || strings.EqualFold(g.Title, ref) {
				return g.Name
			}
		}
		return ref
	}
	after := make([]string, 0, len(e.After))
	for _, ref := range e.After {
		after = append(after, resolve(ref))
	}
	if err := s.SetAfter(resolve(e.Goal), after); err != nil {
		return ask(
			e.Task,
			fmt.Sprintf("Triage wanted goal %s to wait for %s, but %v. Should it wait, "+
				"and for what?", e.Goal, strings.Join(e.After, ", "), err),
		)
	}
	return nil
}

// feedback passes text to the grilling of a goal in planning, or asks the
// human when the goal isn't one.
func (h *Harness) feedback(
	s *queue.Store,
	task, goal, text string,
	ask func(task, text string) error,
) error {
	g, err := s.Goal(goal)
	if err != nil || g.State != queue.GoalPlanning {
		return ask(task, fmt.Sprintf("Triage had feedback for goal %q, which isn't being planned. "+
			"Where should it go?\n\n%s", goal, text))
	}
	_, err = intake.Write(
		feedbackDir(s, goal),
		intake.Intake{Source: "triage", Created: h.now(), Text: text},
	)
	return err
}

// applyAdd adds a task triage handed in to its goal. A task for a goal still
// in planning is feedback for its grilling instead. A task for a goal that
// is done, on a workstream the goal doesn't have, or after a task that doesn't
// exist goes to the human: adding a workstream or changing dependencies needs
// their approval (ADR 0010).
func (h *Harness) applyAdd(repo Repo, e session.Entry, ask func(task, text string) error) error {
	s := repo.Store
	g, err := s.Goal(e.Goal)
	if err == nil && g.State == queue.GoalPlanning {
		return h.feedback(s, e.Task, g.Name, "Add a task to the plan: "+e.Title+"\n\n"+e.Text, ask)
	}
	var problems []string
	var existing []*queue.Task
	switch {
	case err != nil:
		problems = append(problems, fmt.Sprintf("there is no goal %q", e.Goal))
	case g.State == queue.GoalDone || g.State == queue.GoalFinished:
		problems = append(problems, fmt.Sprintf("goal %s is %s", g.Name, g.State))
	default:
		if _, ok := g.Workstream(e.Workstream); !ok {
			problems = append(
				problems,
				fmt.Sprintf("goal %s has no workstream %q", g.Name, e.Workstream),
			)
		}
		if existing, err = s.Tasks(g.Name); err != nil {
			return err
		}
	}
	for _, id := range e.After {
		if len(problems) == 0 &&
			!slices.ContainsFunc(existing, func(t *queue.Task) bool { return t.ID == id }) {
			problems = append(problems, "there is no task "+id+" to come after")
		}
	}
	if _, ok := repo.Config.Profiles[e.Profile]; e.Profile != "" && !ok {
		problems = append(problems, fmt.Sprintf("there is no profile %q", e.Profile))
	}
	if len(problems) > 0 {
		return ask(
			e.Task,
			fmt.Sprintf("Triage wanted to add the task %q to goal %s on workstream %s, "+
				"but %s. Should it be added, and where?\n\n%s", e.Title, e.Goal, e.Workstream,
				strings.Join(problems, " and "), e.Text),
		)
	}
	return s.AddTask(g.Name, &queue.Task{
		Title:      e.Title,
		Kind:       queue.Planned,
		Profile:    e.Profile,
		Workstream: e.Workstream,
		DependsOn:  e.After,
		Origin:     queue.Origin{Type: "triage", Ref: e.Task},
		Created:    h.now(),
		Body:       e.Text,
	})
}

// startGoal starts a goal a session handed in. It is grilled first, unless
// its plan was handed in too because the work was already decided: then the
// plan waits for the human's sign-off straight away.
func (h *Harness) startGoal(
	ctx context.Context,
	repo Repo,
	e session.Entry,
	origin queue.Origin,
) (*queue.Goal, error) {
	s := repo.Store
	body := e.Text
	if body == "" {
		body = e.Title
	}
	g, err := plan.NewGoal(ctx, s, "", e.Title, e.Description, body, origin, h.now())
	if err != nil {
		return nil, err
	}
	h.log().Info("new goal", "repo", s.Repo(), "goal", g.Name, "from", origin.Type,
		"planned", e.Plan != "")
	if e.Plan == "" {
		return g, nil
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil || len(tasks) == 0 {
		return g, err
	}
	grill := tasks[0]
	p, err := plan.Parse([]byte(e.Plan))
	if err == nil {
		err = p.Validate(repo.Config)
	}
	if err != nil {
		// Grilling starts from the draft instead.
		grill.Body = appendSection(grill.Body, "A plan triage drafted, which didn't hold up",
			err.Error()+"\n\n```yaml\n"+strings.TrimSpace(e.Plan)+"\n```")
		return g, s.SaveTask(g.Name, grill)
	}
	if err := plan.Save(s.GoalDir(g.Name), p); err != nil {
		return g, err
	}
	grill.Body = appendSection(
		grill.Body,
		"Planned by triage",
		"The work was already decided, so triage handed in the plan and there was nothing to grill.",
	)
	return g, s.Move(g.Name, grill, queue.Done)
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
		err = p.Validate(repo.Config)
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
func doneIntake(repo, name string) error {
	in, err := intake.Read(filepath.Join(intake.Dir(repo), name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return intake.Done(in)
}
