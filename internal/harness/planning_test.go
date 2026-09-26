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

func (f *fixture) queueIntake(t *testing.T, goal, text string) {
	t.Helper()
	if _, err := intake.Write(
		intake.Dir(f.main.Dir, goal),
		intake.Intake{Source: "pane", Created: time.Now(), Text: text},
	); err != nil {
		t.Fatal(err)
	}
}

func TestNewGoalIsGrilledAndSignedOff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.queueIntake(t, "", "Implement the Grim Reminders set\n\nAll of its cards.")

	round := 0
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
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
		task := s.spec.Tasks[0]
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

	// The intake becomes a goal in planning, and its first round asks.
	f.step()
	goals, _ := f.store.Goals()
	var g *queue.Goal
	for _, goal := range goals {
		if goal.Name != "set" {
			g = goal
		}
	}
	if g == nil || g.Name != "implement-the-grim-reminders-set" || g.State != queue.GoalPlanning {
		t.Fatalf("goals = %+v", goals)
	}
	if pending, _ := intake.Pending(intake.Dir(f.main.Dir, "")); len(pending) != 0 {
		t.Error("the repo intake was not moved to done")
	}
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

func TestTriageSortsIntake(t *testing.T) {
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
		s.entry(
			session.Entry{
				Type:       session.EntryAdd,
				Task:       task,
				Title:      "Weaken ward",
				Workstream: "engine",
				After:      []string{existing.ID},
				Text:       "Ward stops only 1 damage.",
			},
		)
		s.entry(
			session.Entry{
				Type:       session.EntryAdd,
				Task:       task,
				Title:      "Poison counter",
				Workstream: "web",
				Text:       "Needs a UI.",
			},
		)
		s.entry(
			session.Entry{
				Type:  session.EntryGoal,
				Task:  task,
				Title: "Build a web UI",
				Text:  "A browser client.",
			},
		)
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
	var weaken, triage *queue.Task
	for _, task := range tasks {
		switch {
		case task.Title == "Weaken ward":
			weaken = task
		case task.Kind == queue.Triage:
			triage = task
		}
	}
	if weaken == nil || weaken.Workstream != "engine" || weaken.DependsOn[0] != existing.ID ||
		weaken.Origin.Type != "triage" {
		t.Errorf("weaken task = %+v", weaken)
	}
	// The task on a workstream the goal lacks went to the human instead, so
	// triage waits on that question.
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if triage == nil || triage.State != queue.Blocked || len(open) != 1 ||
		!strings.Contains(open[0].Text, `no workstream "web"`) {
		t.Errorf("triage = %+v, questions = %+v", triage, open)
	}
	if _, err := f.store.Goal("build-a-web-ui"); err != nil {
		t.Errorf("the new goal was not started: %v", err)
	}
	// Blocked on its question, triage is not done, so the intake waits too.
	if pending, _ := intake.Pending(intake.Dir(f.main.Dir, "set")); len(pending) != 1 {
		t.Errorf("pending intake = %+v", pending)
	}
}

func TestTriageDoneMovesIntake(t *testing.T) {
	f := newFixture(t)
	f.queueIntake(t, "set", "Rename the ward keyword to shield.")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		s.entry(
			session.Entry{
				Type:       session.EntryAdd,
				Task:       s.spec.Tasks[0],
				Title:      "Rename ward",
				Workstream: "engine",
			},
		)
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	f.step()
	if pending, _ := intake.Pending(intake.Dir(f.main.Dir, "set")); len(pending) != 0 {
		t.Errorf("a triaged intake is still pending: %+v", pending)
	}
	if got := f.step(); len(got) != 1 || got[0].Kind != queue.Planned {
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
	f.queueIntake(t, g.Name, "Split cards into two workstreams.")
	var sawFeedback bool
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		task, _ := f.store.Task(g.Name, s.spec.Tasks[0])
		sawFeedback = strings.Contains(task.Body, "Split cards into two workstreams.")
		s.report(session.EntryAsk, s.spec.Tasks[0], "Split how?")
	}
	f.step()
	if !sawFeedback {
		t.Error("the next round did not see the feedback")
	}
	if p, _ := plan.Load(f.store.GoalDir(g.Name)); p != nil {
		t.Error("the old plan was not set aside")
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
	cfg, _ := config.Load(f.main.Dir, f.h.Paths)
	tasks, _ := f.store.Tasks(g.Name)
	for _, kind := range []queue.Kind{queue.Grilling, queue.Triage} {
		p, err := f.h.planningPrompt(Repo{Store: f.store, Config: cfg}, g,
			PromptInput{Goal: g, Batch: schedule.Batch{Kind: kind, Tasks: tasks}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p, "Nobody reads your replies") {
			t.Errorf("%s prompt lacks the unattended rule", kind)
		}
		if kind == queue.Grilling && !strings.Contains(p, "never in a reply") {
			t.Error("grilling prompt doesn't route the skill's questions through the tool")
		}
	}
	if !strings.Contains(
		Prompt(PromptInput{Goal: g, Batch: schedule.Batch{}}),
		"Nobody reads your replies",
	) {
		t.Error("the work prompt lacks the unattended rule")
	}
}
