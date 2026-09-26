package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

func TestGoalWaitsForOthersToFinish(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.store.CreateGoal(&queue.Goal{Name: "later", Title: "Later", State: queue.GoalActive,
		Base: "main", Workstreams: []queue.Workstream{{Name: "engine"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddTask("later", &queue.Task{Title: "Build on it", Kind: queue.Planned,
		Workstream: "engine"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetAfter("later", []string{"set"}); err != nil {
		t.Fatal(err)
	}
	f.add("engine", "Add ward")
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, s.spec.Goal+".txt", "work\n")
		s.report(session.EntryDone, s.spec.Tasks[0], "")
	}
	for _, b := range f.step() {
		if b.Goal == "later" {
			t.Fatal("a waiting goal's work started")
		}
	}

	// set lands upstream: main on the remote gets a commit the local main
	// hasn't pulled.
	bare := git.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{{"init", "--bare", "--initial-branch=main"}} {
		if _, err := bare.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", bare.Dir}, {"push", "--quiet", "-u", "origin", "main"},
		{"checkout", "--quiet", "--detach"},
	} {
		if _, err := f.main.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, f.main.Dir, "landed.txt", "set's work\n")
	for _, args := range [][]string{
		{"add", "landed.txt"}, {"commit", "--quiet", "-m", "feat: land set"},
		{"push", "--quiet", "origin", "HEAD:main"}, {"checkout", "--quiet", "main"}, {"fetch", "--quiet", "origin"},
	} {
		if _, err := f.main.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := f.store.Goal("set")
	g.State = queue.GoalDone
	if err := f.store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	if got := f.step(); len(got) != 0 {
		t.Fatalf("done isn't finished, but work started: %+v", got)
	}
	g.State = queue.GoalFinished
	if err := f.store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	if got := f.step(); len(got) != 1 || got[0].Goal != "later" {
		t.Fatalf("once set finished = %+v, want later's task", got)
	}
	if f.show("diatom/later/integration", "landed.txt") != "set's work" {
		t.Error("the goal's branch didn't start from what landed upstream")
	}
}

func TestTriageOrdersGoals(t *testing.T) {
	f := newFixture(t)
	f.queueIntake(t, "", "Do the catalog, then the sweep.")
	f.agent.act = func(_ *testing.T, _ string, s agentSession) {
		task := s.spec.Tasks[0]
		s.entry(session.Entry{Type: session.EntryGoal, Task: task, Title: "The sweep",
			After: []string{"The catalog"}})
		s.entry(session.Entry{Type: session.EntryGoal, Task: task, Title: "The catalog"})
		s.entry(
			session.Entry{
				Type:  session.EntryAfter,
				Task:  task,
				Goal:  "set",
				After: []string{"The sweep"},
			},
		)
		s.entry(
			session.Entry{
				Type:  session.EntryAfter,
				Task:  task,
				Goal:  "The catalog",
				After: []string{"set"},
			},
		)
		s.report(session.EntryDone, task, "")
	}
	f.step()
	byTitle := map[string]*queue.Goal{}
	goals, _ := f.store.Goals()
	for _, g := range goals {
		byTitle[g.Title] = g
	}
	sweep, catalog := byTitle["The sweep"], byTitle["The catalog"]
	if sweep == nil || catalog == nil || strings.Join(sweep.After, ",") != catalog.Name {
		t.Fatalf("goals = %+v", byTitle)
	}
	if set := byTitle["Implement the set"]; strings.Join(set.After, ",") != sweep.Name {
		t.Errorf("set after = %v", set.After)
	}
	// The catalog waiting for set would close a loop, so it's a question.
	open, _ := f.store.Questions(queue.IntakeGoal, queue.QuestionOpen)
	if len(open) != 1 || !strings.Contains(open[0].Text, "in a loop") || len(catalog.After) != 0 {
		t.Errorf("questions = %+v, catalog after = %v", open, catalog.After)
	}
}
