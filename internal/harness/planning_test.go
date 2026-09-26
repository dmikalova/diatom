package harness

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/schedule"
	"github.com/dmikalova/diatom/internal/session"
)

const goodPlan = `summary: Ward and a card that uses it.
workstreams:
  - name: engine
  - name: cards
    dependsOn: [engine]
tasks:
  - key: ward
    title: Add the ward keyword
    workstream: engine
  - key: warden
    title: Implement Warden
    workstream: cards
`

func (s agentSession) entry(e session.Entry) {
	s.t.Helper()
	if err := session.Append(s.dir, s.spec, e); err != nil {
		s.t.Fatal(err)
	}
}

// queueIntake sends text to the repo's intake while looking at goal.
func (f *fixture) queueIntake(t *testing.T, goal, text string) {
	t.Helper()
	if _, err := intake.Write(
		intake.Dir(f.main.Dir),
		intake.Intake{Source: "pane", Created: time.Now(), Goal: goal, Text: text},
	); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) pendingIntake(t *testing.T) int {
	t.Helper()
	items, err := intake.Pending(intake.Dir(f.main.Dir))
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func TestIntakeBecomesAGoalThatIsGrilledAndSignedOff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.queueIntake(t, "", "Implement the Grim Reminders set\n\nAll of its cards.")

	round := 0
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		task := s.spec.Tasks[0]
		if s.spec.Kind == queue.Triage {
			body, _ := os.ReadFile(filepath.Join(s.dir, "prompt.md"))
			if !bytes.Contains(body, []byte("### set: Implement the set (active)")) ||
				!bytes.Contains(body, []byte("Sent with no goal in view.")) {
				t.Errorf("the triage prompt lacks the repo's goals or the hint:\n%s", body)
			}
			s.entry(session.Entry{Type: session.EntryGoal, Task: task,
				Title: "Implement the Grim Reminders set", Text: "All of its cards."})
			s.report(session.EntryDone, task, "")
			return
		}
		if s.spec.Kind != queue.Grilling {
			t.Fatalf("session kind = %s", s.spec.Kind)
		}
		if _, err := os.Stat(filepath.Join(wt, "shared.txt")); err != nil {
			t.Error("the planning worktree lacks the repo's files")
		}
		if _, err := os.Stat(filepath.Join(wt, "scribble.txt")); err == nil {
			t.Error("the planning worktree kept what the last round wrote")
		}
		writeFile(t, wt, "scribble.txt", "thrown away\n")
		round++
		switch round {
		case 1:
			s.report(session.EntryAsk, task, "Which mechanics are new?")
			s.report(session.EntryAsk, task, "Does the engine need a new keyword?")
		case 2:
			body, _ := os.ReadFile(
				filepath.Join(filepath.Dir(s.dir), "..", "tasks", "active", task+".md"),
			)
			if !bytes.Contains(body, []byte("Only ward.")) ||
				!bytes.Contains(body, []byte("Yes, ward.")) {
				t.Errorf("round 2 lacks the answers:\n%s", body)
			}
			s.entry(session.Entry{Type: session.EntryPlan, Task: task, Text: goodPlan})
			s.report(session.EntryDone, task, "")
		}
	}

	// Triage turns the intake into a goal in planning.
	f.step()
	if f.pendingIntake(t) != 0 {
		t.Error("the triaged intake was not moved to done")
	}
	g, err := f.store.Goal("implement-the-grim-reminders-set")
	if err != nil || g.State != queue.GoalPlanning {
		t.Fatalf("goal = %+v, %v", g, err)
	}
	goals, _ := f.store.Goals()
	for _, goal := range goals {
		if goal.Name == queue.IntakeGoal {
			t.Error("the intake goal is listed with the goals")
		}
	}

	// Its first round asks.
	f.step()
	open, _ := f.store.Questions(g.Name, queue.QuestionOpen)
	if len(open) != 2 {
		t.Fatalf("questions = %+v", open)
	}
	// One answer isn't enough to start the next round.
	if err := f.store.Answer(g.Name, open[0].ID, "Only ward.", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := f.step(); len(got) != 0 {
		t.Fatalf("a round started with a question unanswered: %+v", got)
	}
	if err := f.store.Answer(g.Name, open[1].ID, "Yes, ward.", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.step()
	p, err := plan.Load(f.store.GoalDir(g.Name))
	if err != nil || p == nil || len(p.Tasks) != 2 {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if got := f.step(); len(got) != 0 {
		t.Errorf("work started before sign-off: %+v", got)
	}

	cfg, _ := config.Load(f.main.Dir, f.h.Paths)
	if err := plan.Approve(ctx, f.store, cfg, g.Name, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward\n")
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	if got := f.step(); len(got) != 1 || got[0].Goal != g.Name || got[0].Workstream != "engine" {
		t.Errorf("after sign-off = %+v, want the engine task", got)
	}
}

func TestTriageSortsIntakeAcrossGoals(t *testing.T) {
	f := newFixture(t)
	existing := f.add("engine", "Add ward")
	f.queueIntake(
		t,
		"set",
		"Playtest notes: ward felt strong, poison needs a counter, and a web UI would be nice.",
	)
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		if s.spec.Kind != queue.Triage {
			// The planned task runs in the same step; it isn't what this test is about.
			s.report(session.EntryDone, s.spec.Tasks[0], "")
			return
		}
		task := s.spec.Tasks[0]
		body, _ := os.ReadFile(filepath.Join(s.dir, "prompt.md"))
		if !bytes.Contains(body, []byte("Sent while looking at goal set")) {
			t.Errorf("the triage prompt lacks the focus hint:\n%s", body)
		}
		s.entry(session.Entry{Type: session.EntryAdd, Task: task, Goal: "set", Title: "Weaken ward",
			Workstream: "engine", After: []string{existing.ID}, Text: "Ward stops only 1 damage."})
		s.entry(
			session.Entry{Type: session.EntryAdd, Task: task, Goal: "set", Title: "Poison counter",
				Workstream: "web", Text: "Needs a UI."},
		)
		s.entry(session.Entry{Type: session.EntryGoal, Task: task, Title: "Build a web UI",
			Text: "A browser client."})
		s.report(session.EntryDone, task, "")
	}
	batches := f.step()
	kinds := map[queue.Kind]bool{}
	for _, b := range batches {
		kinds[b.Kind] = true
	}
	if !kinds[queue.Triage] {
		t.Fatalf("batches = %+v, want a triage session", batches)
	}

	tasks, _ := f.store.Tasks("set")
	var weaken *queue.Task
	for _, task := range tasks {
		if task.Title == "Weaken ward" {
			weaken = task
		}
	}
	if weaken == nil || weaken.Workstream != "engine" || weaken.DependsOn[0] != existing.ID ||
		weaken.Origin.Type != "triage" {
		t.Errorf("weaken task = %+v", weaken)
	}
	// The task on a workstream the goal lacks went to the human instead, so
	// triage waits on that question.
	triage, _ := f.store.Tasks(queue.IntakeGoal)
	open, _ := f.store.Questions(queue.IntakeGoal, queue.QuestionOpen)
	if len(triage) != 1 || triage[0].State != queue.Blocked || len(open) != 1 ||
		!strings.Contains(open[0].Text, `goal set has no workstream "web"`) {
		t.Errorf("triage = %+v, questions = %+v", triage, open)
	}
	if _, err := f.store.Goal("build-a-web-ui"); err != nil {
		t.Errorf("the new goal was not started: %v", err)
	}
	// Blocked on its question, triage is not done, so the intake waits too.
	if f.pendingIntake(t) != 1 {
		t.Error("the intake was done while triage waits on a question")
	}
}

func TestTriageOfADoneGoalsIntake(t *testing.T) {
	f := newFixture(t)
	g, _ := f.store.Goal("set")
	g.State = queue.GoalDone
	if err := f.store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	f.queueIntake(t, "set", "Rename the ward keyword to shield.")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.entry(session.Entry{Type: session.EntryAdd, Task: s.spec.Tasks[0], Goal: "set",
			Title: "Rename ward", Workstream: "engine"})
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	f.step()
	open, _ := f.store.Questions(queue.IntakeGoal, queue.QuestionOpen)
	if len(open) != 1 || !strings.Contains(open[0].Text, "goal set is done") {
		t.Errorf("questions = %+v, want the human asked where the task goes", open)
	}
}

func TestTriageDoneMovesIntake(t *testing.T) {
	f := newFixture(t)
	f.queueIntake(t, "", "Rename the ward keyword to shield.")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.entry(session.Entry{Type: session.EntryAdd, Task: s.spec.Tasks[0], Goal: "set",
			Title: "Rename ward", Workstream: "engine"})
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	f.step()
	if f.pendingIntake(t) != 0 {
		t.Error("a triaged intake is still pending")
	}
	if got := f.step(); len(got) != 1 || got[0].Kind != queue.Planned || got[0].Goal != "set" {
		t.Errorf("next step = %+v, want the triaged task", got)
	}
}

func TestFeedbackRegrillsAPlan(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := plan.NewGoal(ctx, f.store, "next", "Next set", "Do it.", queue.Origin{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.entry(session.Entry{Type: session.EntryPlan, Task: s.spec.Tasks[0], Text: goodPlan})
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	f.step()
	if p, _ := plan.Load(f.store.GoalDir(g.Name)); p == nil {
		t.Fatal("no plan")
	}

	// Triage passes the human's correction to the goal's grilling.
	f.queueIntake(t, g.Name, "Split cards into two workstreams.")
	var sawFeedback bool
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		task := s.spec.Tasks[0]
		if s.spec.Kind == queue.Triage {
			s.entry(session.Entry{Type: session.EntryFeedback, Task: task, Goal: g.Name,
				Text: "Split cards into two workstreams."})
			s.report(session.EntryDone, task, "")
			return
		}
		t, _ := f.store.Task(g.Name, task)
		sawFeedback = strings.Contains(t.Body, "Split cards into two workstreams.")
		s.report(session.EntryAsk, task, "Split how?")
	}
	f.step() // triage
	f.step() // the next round, with the plan set aside for it
	if p, _ := plan.Load(f.store.GoalDir(g.Name)); p != nil {
		t.Error("the old plan was not set aside")
	}
	if !sawFeedback {
		t.Error("the next round did not see the feedback")
	}
}

func TestTriageStartsADecidedGoal(t *testing.T) {
	f := newFixture(t)
	f.queueIntake(t, "", "Take the purge out of ForgeKey; it is all decided.")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		if s.spec.Kind != queue.Triage {
			t.Errorf("a %s session ran for a decided goal", s.spec.Kind)
			return
		}
		task := s.spec.Tasks[0]
		s.entry(
			session.Entry{
				Type:  session.EntryGoal,
				Task:  task,
				Title: "Take the purge out of ForgeKey",
				Text:  "Decided.",
				Plan:  goodPlan,
			},
		)
		s.entry(session.Entry{Type: session.EntryGoal, Task: task, Title: "Half decided",
			Plan: strings.Replace(goodPlan, "workstream: cards\n", "workstream: nope\n", 1)})
		s.report(session.EntryDone, task, "")
	}
	f.step()
	byTitle := map[string]*queue.Goal{}
	goals, _ := f.store.Goals()
	for _, goal := range goals {
		byTitle[goal.Title] = goal
	}
	g, half := byTitle["Take the purge out of ForgeKey"], byTitle["Half decided"]
	if g == nil || g.State != queue.GoalPlanning || half == nil {
		t.Fatalf("goals = %+v", byTitle)
	}
	if p, _ := plan.Load(f.store.GoalDir(g.Name)); p == nil || len(p.Tasks) != 2 {
		t.Errorf("plan = %+v, want it waiting for sign-off", p)
	}
	if tasks, _ := f.store.Tasks(
		g.Name,
	); tasks[0].Kind != queue.Grilling ||
		tasks[0].State != queue.Done {
		t.Errorf("grilling = %+v, want it done without a round", tasks[0])
	}
	// A draft that doesn't hold up is grilled, starting from it.
	grill, _ := f.store.Tasks(half.Name)
	if len(grill) != 1 || grill[0].State != queue.Pending ||
		!strings.Contains(grill[0].Body, "A plan triage drafted, which didn't hold up") {
		t.Errorf("half-decided grilling = %+v", grill)
	}
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.report(session.EntryAsk, s.spec.Tasks[0], "Which workstream?")
	}
	if got := f.step(); len(got) != 1 || got[0].Goal != half.Name {
		t.Errorf("next step = %+v, want only the half-decided goal grilled", got)
	}
}

func TestGrillingDoneWithoutPlan(t *testing.T) {
	f := newFixture(t)
	g, err := plan.NewGoal(
		context.Background(),
		f.store,
		"next",
		"Next set",
		"",
		queue.Origin{},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.entry(
			session.Entry{
				Type: session.EntryPlan,
				Task: s.spec.Tasks[0],
				Text: strings.Replace(goodPlan,
					"workstream: cards\n", "workstream: cards\n    profile: nope\n", 1),
			},
		)
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	f.step()
	tasks, _ := f.store.Tasks(g.Name)
	if tasks[0].State != queue.Pending ||
		!strings.Contains(tasks[0].Body, `unknown profile "nope"`) ||
		!strings.Contains(tasks[0].Body, "## No plan") {
		t.Errorf("grilling after a bad plan = %s\n%s", tasks[0].State, tasks[0].Body)
	}
}

func TestPlanningPromptsSayOnlyTheToolReachesTheHuman(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g, err := plan.NewGoal(ctx, f.store, "next", "Next set", "", queue.Origin{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.h.ensureIntakeGoal(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	intakeGoal, _ := f.store.Goal(queue.IntakeGoal)
	cfg, _ := config.Load(f.main.Dir, f.h.Paths)
	tasks, _ := f.store.Tasks(g.Name)
	for kind, goal := range map[queue.Kind]*queue.Goal{queue.Grilling: g, queue.Triage: intakeGoal} {
		p, err := f.h.planningPrompt(Repo{Store: f.store, Config: cfg}, goal,
			PromptInput{Goal: goal, Batch: schedule.Batch{Kind: kind, Tasks: tasks}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p, "Nobody reads your replies") {
			t.Errorf("%s prompt lacks the unattended rule", kind)
		}
		if !strings.Contains(p, askGuide) || !strings.Contains(p, orderGuide) ||
			strings.Contains(p, "what order the work goes in") {
			t.Errorf("%s prompt lacks how to ask a question or to order work itself", kind)
		}
		if kind == queue.Grilling && !strings.Contains(p, "never in a reply") {
			t.Error("grilling prompt doesn't route the skill's questions through the tool")
		}
		if kind == queue.Triage && (!strings.Contains(p, "### next: Next set (planning)") ||
			!strings.Contains(p, "task feedback <id> -goal")) {
			t.Errorf("triage prompt lacks the repo's goals or its tools:\n%s", p)
		}
	}
	if work := Prompt(PromptInput{Goal: g, Batch: schedule.Batch{}}); !strings.Contains(work,
		"Nobody reads your replies") || !strings.Contains(work, askGuide) {
		t.Error("the work prompt lacks the unattended rule or how to ask a question")
	}
}
