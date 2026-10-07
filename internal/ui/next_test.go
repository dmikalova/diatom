package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestNextPutsAQuestionOffAndRepeatsAnAnswer covers the two ways past a
// question other than writing an answer: putting it off, and saying an
// earlier question already answered it.
func TestNextPutsAQuestionOffAndRepeatsAnAnswer(t *testing.T) {
	f := newFixture(t)
	for _, text := range []string{"And poison?", "And deathtouch?"} {
		if err := f.store.AddQuestion(
			"set",
			&queue.Question{Task: "0001", Text: text},
		); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := newApp(t, f)
	key(a, "enter")
	// Answer the first, so there is an answer to repeat.
	key(a, "tab")
	typeText(a, "No")
	key(a, "enter")
	if it := a.next.shown(); it == nil || it.q.ID != "0002" {
		t.Fatalf("after answering, Next shows %+v", it)
	}
	if !strings.Contains(plain(a.render()), "l puts it off") {
		t.Errorf("the question doesn't offer its keys:\n%s", plain(a.render()))
	}
	// l puts it off and the question behind it shows.
	key(a, "l")
	if it := a.next.shown(); it == nil || it.q.ID != "0003" {
		t.Fatalf("after l, Next shows %+v", it)
	}
	if last := a.next.items[len(a.next.items)-1]; last.id() != "set question 0002" {
		t.Errorf("the question put off is not last: %s", last.id())
	}
	// s asks first, and only answers on the second press.
	key(a, "s")
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if open[2].Answer != "" {
		t.Fatalf("one s already answered it: %q", open[2].Answer)
	}
	if !strings.Contains(a.next.flash, "s again to answer it as question 0001 was") {
		t.Errorf("s doesn't ask first: %q", a.next.flash)
	}
	key(a, "s")
	open, _ = f.store.Questions("set", queue.QuestionOpen)
	if got := open[2].Answer; !strings.Contains(got, "Question 0001") ||
		!strings.Contains(got, "No") {
		t.Errorf("the repeated answer = %q", got)
	}
}

// TestNextReviewRulesJoinTheNav pins that the reviewer's rule under a
// hunk's head runs on to the nav's border, as Next's own rules do.
func TestNextReviewRulesJoinTheNav(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Answer("set", "0001", "No.", f.env.Now()); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	if it := a.next.shown(); it == nil || it.kind != itemReview {
		t.Fatalf("Next shows %+v", it)
	}
	joined := 0
	for l := range strings.SplitSeq(plain(a.render()), "\n") {
		if strings.Contains(l, "├──") {
			joined++
		}
	}
	if joined < 2 {
		t.Errorf("%d rules join the nav, want Next's and the reviewer's:\n%s", joined,
			plain(a.render()))
	}
}

// TestGraphemeModeRedraws pins that the screen is drawn again once the
// terminal measures graphemes, which the first frames didn't.
func TestGraphemeModeRedraws(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	if _, cmd := a.Update(
		tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet},
	); cmd == nil {
		t.Error("the unicode mode report redraws nothing")
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

func TestNextApprovesAPlan(t *testing.T) {
	f := newFixture(t)
	newPlan(t, f, "grim")
	a, _ := newApp(t, f)
	if it := a.next.shown(); it == nil || it.kind != itemPlan {
		t.Fatalf("Next shows %+v first", it)
	}
	out := plain(a.render())
	for _, want := range []string{"› a  Approve: 1 workstreams, 1 tasks",
		"c  Comment: say what to change", "l  Later"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}
	// a, as on a hunk, while reading the plan.
	key(a, "enter", "a")
	if !strings.Contains(a.next.flash, "a again to approve grim") {
		t.Fatalf("flash = %q", a.next.flash)
	}
	key(a, "a")
	if g, _ := f.store.Goal("grim"); g.State != queue.GoalActive {
		t.Errorf("grim is %s after two a", g.State)
	}
	if it := a.next.shown(); it == nil || it.kind != itemQuestion {
		t.Errorf("after approving, Next shows %+v", it)
	}
}

func TestNextApprovesAPlanFromItsActions(t *testing.T) {
	f := newFixture(t)
	newPlan(t, f, "grim")
	a, _ := newApp(t, f)
	key(a, "enter", "tab", "enter")
	if a.typing() || !strings.Contains(a.next.flash, "enter again to approve grim") {
		t.Fatalf("typing %v, flash = %q", a.typing(), a.next.flash)
	}
	key(a, "enter")
	if g, _ := f.store.Goal("grim"); g.State != queue.GoalActive {
		t.Errorf("grim is %s after two enters", g.State)
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

func TestNextManualSteps(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Answer("set", "0001", "No.", f.env.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddQuestion("set", &queue.Question{Task: "0001", Manual: true,
		Text: "1. Run tofu apply in infra/."}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	out := plain(a.render())
	for _, want := range []string{"👤 Steps to do by hand, for task 0001: Add ward",
		"1. Run tofu apply in infra/.", "👤 1", "👤 set", "1 step to do by hand"} {
		if !strings.Contains(out, want) {
			t.Errorf("the steps lack %q:\n%s", want, out)
		}
	}
	key(a, "enter", "tab", "enter")
	if !strings.Contains(a.next.flash, "enter again to say the steps are done") {
		t.Fatalf("first empty enter: %q", a.next.flash)
	}
	key(a, "enter")
	open, _ := f.store.Questions("set", queue.QuestionOpen)
	if len(open) != 2 || open[1].Answer != "Done." {
		t.Errorf("after two enters = %+v", open)
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
	key(a, "enter", "c")
	if !a.typing() {
		t.Fatal("c opens no comment box")
	}
	key(a, "enter")
	if !strings.Contains(a.next.flash, "say what to change") {
		t.Errorf("an empty comment: %q", a.next.flash)
	}
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

// TestNextPutsAFinishOff pins that a goal ready to finish can wait behind
// the rest, such as questions that came after it was shown.
func TestNextPutsAFinishOff(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "late", State: queue.GoalActive, Base: "main",
		Created: time.Unix(2000, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddDone("late", &queue.Task{Title: "B", Kind: queue.Planned,
		Workstream: "x"}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	// The finish was on screen before the question came.
	a.next.cur = "late 2"
	key(a, "enter")
	a.next.setArea(areaAnswer)
	if out := plain(
		a.render(),
	); !strings.Contains(
		out,
		"l  Later: ask again once the rest are done",
	) {
		t.Fatalf("no later:\n%s", out)
	}
	key(a, "l")
	if it := a.next.shown(); it == nil || it.id() != "set question 0001" ||
		a.next.items[len(a.next.items)-1].id() != "late 2" || !strings.Contains(a.next.flash, "late waits") {
		t.Fatalf("after later: %v, %q", a.next.shown(), a.next.flash)
	}
	// Chosen with enter, it does the same.
	a.next.cur = "late 2"
	a.next.setArea(areaAnswer)
	a.next.act = len(finishActions(*a.next.shown())) - 1
	key(a, "enter")
	if it := a.next.shown(); it == nil || it.id() != "set question 0001" {
		t.Errorf("enter on later showed %v", it)
	}
}

// TestNextMovesToAPlanAfterAHunk pins that a hunk decided is a point to
// move on at: a plan waiting comes before the rest of another goal's review.
func TestNextMovesToAPlanAfterAHunk(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Answer("set", "0001", "No.", f.env.Now()); err != nil {
		t.Fatal(err)
	}
	// A second commit, so the review has hunks left after the first.
	ctx, r := context.Background(), git.Repo{Dir: f.repo}
	write(t, filepath.Join(f.repo, "stack.go"), "package ward\n")
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "feat: stack")
	if err != nil {
		t.Fatal(err)
	}
	task, _ := f.store.Task("set", "0001")
	task.Commits = append(task.Commits, sha)
	if err := f.store.SaveTask("set", task); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	if it := a.next.shown(); it == nil || it.kind != itemReview || it.row.toReview < 2 {
		t.Fatalf("Next shows %+v", it)
	}
	// A plan is handed in while the review is on screen, which keeps it.
	newPlan(t, f, "grim")
	a.Update(tickMsg{})
	if it := a.next.shown(); it == nil || it.kind != itemReview {
		t.Fatalf("the plan took the screen from the review: %+v", it)
	}
	key(a, "enter", "a")
	if it := a.next.shown(); it == nil || it.id() != "grim 0" || a.next.area != areaBody {
		t.Errorf("after a hunk, Next shows %+v, area %d", it, a.next.area)
	}
}

// TestNextPastesIntoTheReviewersComment pins that the terminal's paste, as
// cmd+v sends it, goes into the comment being written on a hunk.
func TestNextPastesIntoTheReviewersComment(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Answer("set", "0001", "No.", f.env.Now()); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	key(a, "enter", "c")
	if rv := a.next.shownReviewer(); rv == nil || !rv.Editing() {
		t.Fatal("c opened no comment box")
	}
	a.Update(tea.PasteMsg{Content: "why ward?"})
	if !strings.Contains(plain(a.render()), "why ward?") || a.next.answer.Value() != "" {
		t.Errorf("the paste went to the answer box %q:\n%s", a.next.answer.Value(),
			plain(a.render()))
	}
}

func TestAnswerQuestionsFromTheGoal(t *testing.T) {
	f := newFixture(t)
	if err := f.store.AddQuestion(
		"set",
		&queue.Question{Task: "0001", Text: "And poison?"},
	); err != nil {
		t.Fatal(err)
	}
	// Another goal's question, which answering on this page never reaches.
	if err := f.store.CreateGoal(&queue.Goal{Name: "late", State: queue.GoalActive, Base: "main",
		Created: time.Unix(2000, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddQuestion(
		"late",
		&queue.Question{Task: "0001", Text: "Later?"},
	); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	key(a, "a")
	if a.selected().kind != entryGoal || a.asking != "set" || a.focus != partMain ||
		a.next.area != areaAnswer || !a.typing() {
		t.Fatalf("a: selected %+v, asking %q, focus %d, area %d", a.selected(), a.asking, a.focus,
			a.next.area)
	}
	if out := plain(a.render()); !strings.Contains(out, "Does ward stack?") {
		t.Errorf("the page doesn't show its question:\n%s", out)
	}
	typeText(a, "yes")
	key(a, "enter")
	if a.asking != "set" || !strings.Contains(plain(a.render()), "And poison?") {
		t.Fatalf("after one answer, asking %q:\n%s", a.asking, plain(a.render()))
	}
	typeText(a, "no")
	key(a, "enter")
	// The goal's own items come next, in Next's order, never another goal's.
	if it := a.next.shown(); a.asking != "set" || it == nil || it.kind == itemQuestion ||
		it.row.goal.Name != "set" {
		t.Fatalf("after the last question, asking %q on %+v", a.asking, it)
	}
	key(a, "esc")
	if a.asking != "" || a.selected().kind != entryGoal || a.status.detail == nil {
		t.Errorf("esc from the goal's items: asking %q on %+v", a.asking, a.selected())
	}
	// esc leaves the questions for the page, too.
	if err := f.store.AddQuestion(
		"set",
		&queue.Question{Task: "0001", Text: "Once more?"},
	); err != nil {
		t.Fatal(err)
	}
	a.reload()
	key(a, "a")
	if a.asking != "set" {
		t.Fatalf("a again: asking %q", a.asking)
	}
	key(a, "esc", "esc")
	if a.asking != "" || a.selected().kind != entryGoal {
		t.Errorf("esc: asking %q on %+v", a.asking, a.selected())
	}
}

func TestNextAsksForMoreWork(t *testing.T) {
	f := newFixture(t)
	if err := f.store.CreateGoal(&queue.Goal{Name: "late", State: queue.GoalActive, Base: "main",
		Created: time.Unix(2000, 0), Workstreams: []queue.Workstream{{Name: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddDone("late", &queue.Task{Title: "B", Kind: queue.Planned,
		Workstream: "x"}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	if it := a.next.shown(); it == nil || it.id() != "late 2" {
		t.Fatalf("a goal ready to finish isn't first: %v", it)
	}
	key(a, "enter")
	a.next.setArea(areaAnswer)
	a.focus = partMain
	key(a, "m")
	if !a.next.more || !a.typing() ||
		!strings.Contains(plain(a.render()), "What more the goal needs") {
		t.Fatalf("m didn't open the box:\n%s", plain(a.render()))
	}
	key(a, "esc")
	if a.next.more {
		t.Error("esc kept the box")
	}
	key(a, "m", "enter")
	if !strings.Contains(a.next.flash, "say what more") {
		t.Errorf("empty = %q", a.next.flash)
	}
	typeText(a, "Also cover the tactic cards")
	key(a, "enter")
	tasks, _ := f.store.Tasks("late")
	var more *queue.Task
	for _, task := range tasks {
		if task.Origin.Type == "finish" {
			more = task
		}
	}
	if more == nil || more.Title != "Also cover the tactic cards" || more.Workstream != "x" ||
		more.State != queue.Pending {
		t.Fatalf("task = %+v", more)
	}
	if it := a.next.shown(); it == nil || it.row.goal.Name == "late" || a.next.more {
		t.Errorf("Next still on the goal: %v", it)
	}
}
