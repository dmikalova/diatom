package roster

import (
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestBriefs(t *testing.T) {
	s := queue.At(t.TempDir(), t.TempDir(), "github.com/me/toy")
	now := time.Unix(100, 0)
	for i, g := range []*queue.Goal{
		{Name: "web", Title: "Web client", Description: "Play in a browser.", State: queue.GoalActive,
			Workstreams: []queue.Workstream{{Name: "ui"}, {Name: "docs", DependsOn: []string{"ui"}}}},
		{Name: "set", Title: "Next set", State: queue.GoalPlanning, After: []string{"web"}},
		{Name: "ward", Title: "Ward", State: queue.GoalPlanning},
		{Name: "old", Title: "Old", State: queue.GoalFinished},
	} {
		g.Created = now.Add(time.Duration(i) * time.Second)
		if err := s.CreateGoal(g); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []*queue.Task{
		{Title: "Board", Kind: queue.Planned, Workstream: "ui"},
		{Title: "Hand", Kind: queue.Planned, Workstream: "ui"},
	} {
		if err := s.AddTask("web", task); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := s.Tasks("web")
	if err := s.Move("web", tasks[0], queue.Done); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTask("set", &queue.Task{
		Title: "Grill", Kind: queue.Grilling, Body: "# Next set\n\nAll 400\ncards of it.\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := plan.Save(s.GoalDir("ward"), &plan.Plan{
		Summary:     strings.Repeat("word ", 50) + "\n\nMore.",
		Workstreams: []queue.Workstream{{Name: "e"}},
		Tasks:       []plan.Task{{Key: "a", Title: "A", Workstream: "e"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddQuestion(
		"web",
		&queue.Question{Task: "0002", Text: "Which board?"},
	); err != nil {
		t.Fatal(err)
	}

	briefs, err := Briefs(s)
	if err != nil {
		t.Fatal(err)
	}
	var list strings.Builder
	if err := Write(&list, briefs, "ward"); err != nil {
		t.Fatal(err)
	}
	want := "- `web`: Web client (active, 1 of 2 tasks done). Play in a browser.\n" +
		"- `set`: Next set (planning, 0 of 1 tasks done; waits for web). All 400 cards of it.\n"
	if list.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", list.String(), want)
	}
	if d := briefs[2].Description; !strings.HasSuffix(d, "word…") ||
		len([]rune(d)) > descriptionWidth+1 {
		t.Errorf("summary fallback = %q", d)
	}

	var detail strings.Builder
	if err := Detail(&detail, s, "web"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- `web`: Web client", "## Workstreams\n\n- ui\n- docs (after ui)\n",
		"- 0001 done [ui]: Board\n", "- 0002 pending [ui]: Hand\n", "1 question(s) wait on the human.",
	} {
		if !strings.Contains(detail.String(), want) {
			t.Errorf("detail lacks %q:\n%s", want, detail.String())
		}
	}
	if err := Detail(&detail, s, "nope"); err == nil {
		t.Error("Detail of an unknown goal succeeded")
	}
}

func TestAbout(t *testing.T) {
	s := queue.At(t.TempDir(), t.TempDir(), "github.com/me/toy")
	g := &queue.Goal{Name: "set", Title: "Next set", Description: "The set after this one.",
		State: queue.GoalPlanning}
	if err := s.CreateGoal(g); err != nil {
		t.Fatal(err)
	}
	if got, _ := About(s, g); got != "The set after this one." {
		t.Errorf("with only a description: %q", got)
	}
	long := strings.Repeat("All of the cards, ", 30)
	if err := s.AddTask("set", &queue.Task{Title: "Grill", Kind: queue.Grilling,
		Body: "# Next set\n\n" + long + "\nplayable.\n\nMore later."}); err != nil {
		t.Fatal(err)
	}
	if got, _ := About(s, g); got != strings.TrimSpace(long)+" playable." {
		t.Errorf("from the brief: %q", got)
	}
	if err := plan.Save(s.GoalDir("set"), &plan.Plan{
		Summary:     "What the plan does,\nand how.\n\nThe rest.",
		Workstreams: []queue.Workstream{{Name: "e"}},
		Tasks:       []plan.Task{{Key: "a", Title: "A", Workstream: "e"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := About(s, g); got != "What the plan does, and how." {
		t.Errorf("from the plan: %q", got)
	}
}
