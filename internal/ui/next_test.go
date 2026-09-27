package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

func TestNextOrdersPlansQuestionsFinishReview(t *testing.T) {
	f := newFixture(t)
	newPlan(t, f, "grim")
	if err := f.store.CreateGoal(
		&queue.Goal{Name: "late", Title: "Late set", State: queue.GoalActive,
			Base: "main", Created: time.Unix(2000, 0)},
	); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddQuestion(
		"late",
		&queue.Question{Task: "0001", Text: "Later?"},
	); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	var got []string
	for _, it := range a.next.items {
		got = append(got, it.id())
	}
	want := []string{"grim 0", "set question 0001", "late question 0001", "set 3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("items = %v, want %v", got, want)
	}
	out := plain(a.render())
	// Next counts what waits across the goals, one kind at a time.
	if !strings.Contains(out, "⏩ Next") || !strings.Contains(out, "   📝 1  ❓ 2  🔎 1") {
		t.Errorf("the nav doesn't count Next's items:\n%s", out)
	}
}

func TestNextAnswersAndMovesOn(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SaveGoal(&queue.Goal{Name: "set", Title: "Implement the next set",
		Description: "Every card of the next set, playable.", State: queue.GoalActive,
		Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddQuestion(
		"set",
		&queue.Question{Task: "0001", Text: "And poison?"},
	); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	out := plain(a.render())
	for _, want := range []string{"Implement the next set", " · set",
		"Every card of the next set, playable.", "Asked by task 0001: Add ward",
		"enter opens the goal", "Does ward stack?", "It matters for poison."} {
		if !strings.Contains(out, want) {
			t.Errorf("the question lacks %q:\n%s", want, out)
		}
	}
	key(a, "enter")
	if a.focus != partMain || a.next.area != areaBody {
		t.Fatalf("enter on Next went to %d/%d", a.focus, a.next.area)
	}
	key(a, "tab")
	typeText(a, "No")
	key(a, "enter")
	qs, _ := f.store.Questions("set", queue.QuestionOpen)
	if qs[0].Answer != "No" {
		t.Errorf("answer = %q", qs[0].Answer)
	}
	// On to the next question, the answer box cleared, reading it first.
	if it := a.next.shown(); it == nil || strings.TrimSpace(it.q.Text) != "And poison?" ||
		a.next.area != areaBody ||
		a.next.answer.Value() != "" {
		t.Errorf("after answering: %+v, area %d", it, a.next.area)
	}
	if !strings.Contains(plain(a.render()), "1 earlier answers") {
		t.Errorf("the next question doesn't count the answer:\n%s", plain(a.render()))
	}
	key(a, "tab")
	typeText(a, "Yes")
	key(a, "enter")
	// The review of the goal's commit is left.
	if it := a.next.shown(); it == nil || it.kind != itemReview {
		t.Errorf("after the questions, Next shows %+v", it)
	}
}

func TestNextOpensTheGoalAndComesBack(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "enter", "shift+tab")
	if a.next.area != areaContext {
		t.Fatalf("shift+tab from the question went to %d", a.next.area)
	}
	key(a, "enter")
	if e := a.selected(); e.kind != entryGoal || a.focus != partMain || !a.fromNext {
		t.Fatalf("enter on the context showed %+v, focus %d", e, a.focus)
	}
	key(a, "esc")
	if a.selected().kind != entryNext || a.focus != partMain || a.next.area != areaBody {
		t.Errorf(
			"esc from the goal went to %+v, focus %d, area %d",
			a.selected(),
			a.focus,
			a.next.area,
		)
	}
	// From the question, esc goes back to the nav.
	key(a, "esc")
	if a.focus != partNav {
		t.Errorf("esc from the question went to %d", a.focus)
	}
}

func TestNextScrollsTheQuestion(t *testing.T) {
	f := newFixture(t)
	var long strings.Builder
	for i := range 60 {
		long.WriteString("line " + strings.Repeat("x", i%7) + "\n")
	}
	long.WriteString("the end\n")
	if err := f.store.AddQuestion(
		"set",
		&queue.Question{Task: "0001", Text: long.String()},
	); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	key(a, "enter")
	// The long question is second; answer the first to reach it.
	key(a, "tab")
	typeText(a, "No")
	key(a, "enter")
	a.render()
	key(a, " ")
	if a.next.scroll != a.next.room/2 {
		t.Errorf("space scrolled to %d, room %d", a.next.scroll, a.next.room)
	}
	key(a, "shift+space")
	if a.next.scroll != 0 {
		t.Errorf("shift+space scrolled back to %d", a.next.scroll)
	}
	key(a, " ")
	a.Update(tea.MouseWheelMsg{X: 60, Y: 12, Button: tea.MouseWheelDown})
	for range 20 {
		key(a, " ")
	}
	if out := plain(a.render()); !strings.Contains(out, "the end") ||
		!strings.Contains(out, "more above") {
		t.Errorf("scrolled to the end:\n%s", out)
	}
}

func TestNextSignsOffAPlan(t *testing.T) {
	f := newFixture(t)
	newPlan(t, f, "grim")
	a, _ := newApp(t, f)
	if it := a.next.shown(); it == nil || it.kind != itemPlan {
		t.Fatalf("Next shows %+v first", it)
	}
	key(a, "enter", "tab", "enter")
	if !strings.Contains(a.next.flash, "enter again to sign off grim") {
		t.Fatalf("flash = %q", a.next.flash)
	}
	key(a, "enter")
	if g, _ := f.store.Goal("grim"); g.State != queue.GoalActive {
		t.Errorf("grim is %s after two enters", g.State)
	}
	if it := a.next.shown(); it == nil || it.kind != itemQuestion {
		t.Errorf("after signing off, Next shows %+v", it)
	}
}

func TestIntakeSendsWhatIsOnScreen(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "i")
	typeText(a, "poison stacks too")
	key(a, "enter")
	items, _ := intake.Pending(intake.Dir(f.repo))
	if len(items) != 1 || items[0].Goal != "set" ||
		!strings.Contains(items[0].Context, "question 0001 of set") ||
		!strings.Contains(items[0].Context, "Does ward stack? It matters for poison.") {
		t.Errorf("intake = %+v", items)
	}
}

func TestIntakeBoxGrows(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "i")
	typeText(a, strings.Repeat("word ", 30))
	if h := a.intakeHeight(); h < 4 || h > a.height/2 {
		t.Errorf("intake box is %d lines for 150 characters", h)
	}
	key(a, "esc")
	if h := a.intakeHeight(); h != 1 {
		t.Errorf("unfocused, the intake box is %d lines", h)
	}
}

func TestNextOffersToFinishAGoal(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "ward", Title: "Ward", State: queue.GoalActive,
		Base: "main", Created: time.Unix(10, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddTask("ward", &queue.Task{Title: "Add it", Kind: queue.Planned,
		Workstream: "engine"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := f.store.Tasks("ward")
	if err := f.store.Move("ward", tasks[0], queue.Done); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateGoal(
		&queue.Goal{Name: "poison", Title: "Poison", State: queue.GoalActive,
			Base: "main", After: []string{"ward"}, Created: time.Unix(20, 0)},
	); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	var finish *item
	for i := range a.next.items {
		if a.next.items[i].kind == itemFinish {
			finish = &a.next.items[i]
		}
	}
	if finish == nil || finish.row.goal.Name != "ward" {
		t.Fatalf("items = %+v", a.next.items)
	}
	a.next.cur = finish.id()
	out := plain(a.render())
	for _, want := range []string{"All its work is done and reviewed", "Finishing unblocks: poison",
		"› P  Merge it into main", "F  Open its stacked pull requests",
		"d  Mark it done, to land later"} {
		if !strings.Contains(out, want) {
			t.Errorf("the finish item lacks %q:\n%s", want, out)
		}
	}
	key(a, "enter", "tab")
	if a.next.area != areaAnswer || a.typing() {
		t.Fatalf("tab to the actions: area %d, typing %v", a.next.area, a.typing())
	}
	key(a, "enter")
	if !strings.Contains(a.status.flash, "enter again to merge ward into main") {
		t.Errorf("first enter: %q", a.status.flash)
	}
	if _, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Error("a second enter doesn't start finishing the goal")
	}
}

func TestNextAnswersOnSeveralLines(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "enter", "tab", "enter")
	if it := a.next.shown(); it == nil || it.kind != itemQuestion || a.next.area != areaAnswer {
		t.Fatal("an empty answer moved on")
	}
	typeText(a, "No, it never")
	key(a, "shift+enter")
	typeText(a, "stacks.")
	key(a, "enter")
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if len(open) != 1 || open[0].Answer != "No, it never\nstacks." {
		t.Errorf("answer = %+v", open)
	}
}

func TestNextSendsAPlanBack(t *testing.T) {
	f := newFixture(t)
	newPlan(t, f, "hex")
	a, _ := newApp(t, f)
	key(a, "enter", "tab")
	typeText(a, "Split the engine.")
	key(a, "enter")
	fb, err := intake.Pending(plan.FeedbackDir(f.store.GoalDir("hex")))
	if err != nil || len(fb) != 1 || fb[0].Text != "Split the engine." {
		t.Fatalf("feedback = %+v, %v", fb, err)
	}
	if g, _ := f.store.Goal("hex"); g.State != queue.GoalPlanning ||
		!strings.Contains(a.next.flash, "sent back to hex's grilling") {
		t.Errorf("hex is %s, flash %q", g.State, a.next.flash)
	}
	// The plan waits for grilling now, not for the human.
	if it := a.next.shown(); it == nil || it.kind != itemQuestion {
		t.Errorf("after sending the plan back, Next shows %+v", it)
	}
	openGoal(t, a, "hex")
	if out := plain(a.render()); !strings.Contains(out, "plan sent back with your changes") {
		t.Errorf("the goal's page:\n%s", out)
	}
}

// TestNextOpensTheNextItemOnItself pins that when the item shown goes, as a
// goal landed from its actions does, the next opens with its body in hand,
// though the actions had the keyboard.
func TestNextOpensTheNextItemOnItself(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := git.Repo{Dir: f.repo}
	write(t, filepath.Join(f.repo, "b.txt"), "b\n")
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "feat: b")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateGoal(&queue.Goal{Name: "late", State: queue.GoalActive, Base: "main",
		Created: time.Unix(2000, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddTask("late", &queue.Task{Title: "B", Kind: queue.Planned, Workstream: "x",
		Commits: []string{sha}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Answer("set", "0001", "yes", time.Now()); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	key(a, "enter")
	// The keyboard on an area the review has none of, as a finish item's
	// actions leave it.
	a.next.area = areaAnswer
	a.next.acted()
	bar := tui.Color("▌ ", tui.Accent)
	if a.next.area != areaBody || !strings.Contains(a.render(), bar) {
		t.Fatalf("area %d, no bar", a.next.area)
	}
	a.next.area = areaAnswer
	a.next.cur = "gone"
	a.next.reload()
	if a.next.area != areaBody || a.next.shown().id() != "set 3" {
		t.Errorf("area %d on %s", a.next.area, a.next.shown().id())
	}
	// The review takes the keys, and on its last hunk moves on to the next
	// goal's, still in hand.
	key(a, "a")
	if a.next.shown().id() != "late 3" || a.next.area != areaBody ||
		!strings.Contains(a.render(), bar) {
		t.Errorf("moved to %s, area %d", a.next.shown().id(), a.next.area)
	}
}

// TestReviewNotesHoldTheLanding pins that a goal whose approved hunks carried
// comments isn't offered to land until triage has sorted them: they may
// bring it more work.
func TestReviewNotesHoldTheLanding(t *testing.T) {
	f := newFixture(t)
	for _, in := range []intake.Intake{
		{Source: "review", Goal: "set", Created: time.Unix(10, 0), Text: "check the shark"},
		{Source: "pane", Goal: "set", Created: time.Unix(11, 0), Text: "just a hint"},
	} {
		if _, err := intake.Write(intake.Dir(f.repo), in); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := newApp(t, f)
	var row goalRow
	for _, r := range a.status.rows {
		if r.goal.Name == "set" {
			row = r
		}
	}
	if row.notes != 1 {
		t.Fatalf("notes = %d, want only the review's", row.notes)
	}
	row.counts = map[queue.State]int{queue.Done: 1}
	row.questions, row.toReview, row.ready, row.activeWork = 0, 0, 0, nil
	if name, _ := goalStatus(row); name != "reviewing" || readyToFinish(row) ||
		plain(relevant(row)) != "triage is sorting your review notes" {
		t.Errorf("status %q, ready %v, %q", name, readyToFinish(row), plain(relevant(row)))
	}
	row.notes = 0
	if !readyToFinish(row) {
		t.Error("sorted, it isn't ready to finish")
	}
	row.goal = &queue.Goal{Name: "set", State: queue.GoalDone}
	row.notes = 1
	if readyToFinish(row) {
		t.Error("a done goal with notes is ready to land")
	}
}
