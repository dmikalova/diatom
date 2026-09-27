package panes

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/queue"
)

// newApp opens the fixture's repo in an app whose scheduler only records
// being stopped.
func newApp(t *testing.T, f *fixture) (*App, *bool) {
	t.Helper()
	stopped := false
	done := make(chan struct{})
	a := NewApp(context.Background(), f.env, Scheduler{
		Stop: func() {
			if !stopped {
				stopped = true
				close(done)
			}
		},
		Done: done,
	})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return a, &stopped
}

func TestAppNav(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SaveGoal(&queue.Goal{Name: "set", Title: "Implement the next set",
		State: queue.GoalActive, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	out := ansi.Strip(a.render())
	for _, want := range []string{"» Next", "+ Intake", "Implement the next set", "?1", "±1",
		"$0.00 · 0 running", "─ intake ─"} {
		if !strings.Contains(out, want) {
			t.Errorf("app lacks %q:\n%s", want, out)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != 120 {
			t.Errorf("line %d is %d wide: %q", i, w, line)
		}
	}
	// Next shows the question waiting.
	if !strings.Contains(out, "Does ward stack?") {
		t.Errorf("Next doesn't show the question:\n%s", out)
	}
	// Moving down the nav shows each entry in the main pane.
	key(a, "j", "j")
	if e := a.selected(); e.kind != entryGoal || !strings.Contains(ansi.Strip(a.render()),
		"‹ Implement the next set") {
		t.Errorf("selected %+v:\n%s", e, ansi.Strip(a.render()))
	}
	// Into the goal, down its menu, and back out to the nav.
	key(a, "enter")
	if a.focus != partMain {
		t.Fatalf("focus = %d after enter", a.focus)
	}
	key(a, "j", "esc")
	if a.focus != partNav || a.status.detail == nil {
		t.Errorf("esc from a goal: focus %d, detail %v", a.focus, a.status.detail)
	}
}

func TestAppTabsThroughItsParts(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	for _, want := range []part{partMain, partIntake, partNav} {
		key(a, "tab")
		if a.focus != want {
			t.Errorf("tab went to %d, want %d", a.focus, want)
		}
	}
	key(a, "shift+tab")
	if a.focus != partIntake {
		t.Errorf("shift+tab went to %d", a.focus)
	}
	// Letters are the intake's text, and enter sends it.
	typeText(a, "quit halving")
	key(a, "enter")
	items, _ := intake.Pending(intake.Dir(f.repo))
	if len(items) != 1 || items[0].Text != "quit halving" {
		t.Errorf("intake = %+v", items)
	}
	// i reaches the intake from anywhere but a text box.
	key(a, "esc")
	if a.focus != partNav {
		t.Errorf("esc from the intake went to %d", a.focus)
	}
	key(a, "i")
	if a.focus != partIntake || a.intake.area.Value() != "" {
		t.Errorf("i: focus %d, text %q", a.focus, a.intake.area.Value())
	}
}

func TestAppQuitSuspends(t *testing.T) {
	f := newFixture(t)
	a, stopped := newApp(t, f)
	key(a, "tab", "enter")
	// Answering, q is a letter of the answer.
	typeText(a, "q")
	if *stopped || a.questions.area.Value() != "q" {
		t.Fatalf("q while answering: stopped %v, answer %q", *stopped, a.questions.area.Value())
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !*stopped || !a.quitting || cmd == nil ||
		!strings.Contains(ansi.Strip(a.render()), "Suspending") {
		t.Fatalf("ctrl+c: stopped %v, quitting %v", *stopped, a.quitting)
	}
	if _, ok := cmd().(stoppedMsg); !ok {
		t.Error("quitting doesn't wait for the scheduler to stop")
	}
	if _, cmd := a.Update(stoppedMsg{}); cmd == nil || a.StoppedAtOnce() {
		t.Error("the app didn't quit once the scheduler stopped")
	}
	key(a, "ctrl+c")
	if !a.StoppedAtOnce() {
		t.Error("a second ctrl+c doesn't stop at once")
	}
}

func TestAppMouse(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	lines, _ := a.navLines()
	goalLine := -1
	for i, l := range lines {
		if l.entry >= 0 && a.entries()[l.entry].kind == entryGoal {
			goalLine = i
		}
	}
	a.Update(tea.MouseClickMsg{X: 3, Y: goalLine, Button: tea.MouseLeft})
	if a.selected().kind != entryGoal || a.focus != partNav {
		t.Errorf("clicking the goal selected %+v, focus %d", a.selected(), a.focus)
	}
	a.Update(tea.MouseClickMsg{X: 60, Y: 3, Button: tea.MouseLeft})
	if a.focus != partMain {
		t.Errorf("clicking the main pane gave focus to %d", a.focus)
	}
	a.Update(tea.MouseClickMsg{X: 3, Y: 29, Button: tea.MouseLeft})
	if a.focus != partIntake {
		t.Errorf("clicking the intake box gave focus to %d", a.focus)
	}
	a.Update(tea.MouseWheelMsg{X: 3, Y: 3, Button: tea.MouseWheelUp})
	if a.sel != 1 {
		t.Errorf("the wheel over the nav moved to %d", a.sel)
	}
}

func TestAppViewer(t *testing.T) {
	f := newFixture(t)
	a := NewApp(context.Background(), f.env, Scheduler{Viewer: "diatom is open elsewhere"})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if out := ansi.Strip(a.render()); !strings.Contains(out, "viewer:") ||
		!strings.Contains(out, "diatom is open elsewhere") {
		t.Errorf("a viewer doesn't say so:\n%s", ansi.Strip(a.render()))
	}
	if _, cmd := a.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil || a.quitting {
		t.Error("a viewer doesn't quit straight away")
	}
}
