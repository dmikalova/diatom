package queue

import (
	"strings"
	"testing"
)

func TestSetAfterAndWaiting(t *testing.T) {
	s := Open(t.TempDir())
	for _, name := range []string{"a", "b", "c"} {
		if err := s.CreateGoal(&Goal{Name: name, State: GoalActive}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAfter("c", []string{"a", "b", "a"}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Goal("c")
	if strings.Join(c.After, ",") != "a,b" {
		t.Errorf("after = %v", c.After)
	}
	for after, want := range map[string]string{
		"c":    "can't wait for itself",
		"nope": "no goal nope",
	} {
		if err := s.SetAfter(
			"c",
			[]string{after},
		); err == nil ||
			!strings.Contains(err.Error(), want) {
			t.Errorf("SetAfter(c, %s) = %v", after, err)
		}
	}
	if err := s.SetAfter(
		"a",
		[]string{"c"},
	); err == nil ||
		!strings.Contains(err.Error(), "a → c → a") {
		t.Errorf("a loop = %v", err)
	}

	if w := s.Waiting(c); strings.Join(w, ",") != "a,b" {
		t.Errorf("waiting = %v", w)
	}
	a, _ := s.Goal("a")
	a.State = GoalFinished
	if err := s.SaveGoal(a); err != nil {
		t.Fatal(err)
	}
	// Done isn't enough: only finished, merged upstream, is.
	b, _ := s.Goal("b")
	b.State = GoalDone
	if err := s.SaveGoal(b); err != nil {
		t.Fatal(err)
	}
	if w := s.Waiting(c); strings.Join(w, ",") != "b" {
		t.Errorf("waiting once a finished = %v", w)
	}
	if err := s.SetAfter("c", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Goal("c"); len(c.After) != 0 {
		t.Error("SetAfter with none didn't clear it")
	}
}
