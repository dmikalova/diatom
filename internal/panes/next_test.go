package panes

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/queue"
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
	out := ansi.Strip(a.render())
	if !strings.Contains(out, "» Next                       4") {
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
	out := ansi.Strip(a.render())
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
	if !strings.Contains(ansi.Strip(a.render()), "1 earlier answers") {
		t.Errorf("the next question doesn't count the answer:\n%s", ansi.Strip(a.render()))
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
	a.Update(tea.MouseWheelMsg{X: 60, Y: 12, Button: tea.MouseWheelDown})
	for range 20 {
		key(a, " ")
	}
	if out := ansi.Strip(a.render()); !strings.Contains(out, "the end") ||
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
	out := ansi.Strip(a.render())
	for _, want := range []string{"All its work is done and reviewed", "Finishing unblocks: poison",
		"d  Mark it done and lay it out for landing"} {
		if !strings.Contains(out, want) {
			t.Errorf("the finish item lacks %q:\n%s", want, out)
		}
	}
	key(a, "enter", "tab")
	if a.next.area != areaAnswer || a.typing() {
		t.Fatalf("tab to the actions: area %d, typing %v", a.next.area, a.typing())
	}
	key(a, "enter")
	if !strings.Contains(a.status.flash, "enter again to mark ward done") {
		t.Errorf("first enter: %q", a.status.flash)
	}
	if _, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Error("a second enter doesn't start finishing the goal")
	}
}
