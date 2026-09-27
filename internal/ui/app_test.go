package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/intake"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
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
	for _, want := range []string{"⏩ Next", "➕ Intake", "⏳ Implement the next set", "    1 question",
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
	// Next's question has three areas between the nav and the intake box.
	for _, want := range []struct {
		part part
		area nextArea
	}{{partMain, areaContext}, {partMain, areaBody}, {partMain, areaAnswer}, {partIntake, 0}, {partNav, 0}} {
		key(a, "tab")
		if a.focus != want.part || want.part == partMain && a.next.area != want.area {
			t.Errorf("tab went to %d/%d, want %d/%d", a.focus, a.next.area, want.part, want.area)
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
	key(a, "enter", "tab")
	// Answering, q is a letter of the answer.
	typeText(a, "q")
	if *stopped || a.next.answer.Value() != "q" {
		t.Fatalf("q while answering: stopped %v, answer %q", *stopped, a.next.answer.Value())
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
	a.renderNav()
	goalLine := -1
	for y, e := range a.rowEntry {
		if e >= 0 && a.entries()[e].kind == entryGoal {
			goalLine = y
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
	a.Update(wheelMsg{})
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

func TestAppGathersTheWheel(t *testing.T) {
	f := newFixture(t)
	for i := range 12 {
		if err := f.store.CreateGoal(&queue.Goal{Name: fmt.Sprintf("g%02d", i),
			State: queue.GoalActive}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := newApp(t, f)
	a.View()
	// A fast spin schedules one scroll, and draws nothing until it lands.
	var scheduled int
	for range 8 {
		if _, cmd := a.Update(
			tea.MouseWheelMsg{X: 3, Y: 3, Button: tea.MouseWheelDown},
		); cmd != nil {
			scheduled++
		}
	}
	if scheduled != 1 || a.dirty || a.sel != 0 {
		t.Fatalf("spinning scheduled %d scrolls, dirty %v, moved to %d", scheduled, a.dirty, a.sel)
	}
	a.Update(wheelMsg{})
	if a.sel != 8 || !a.dirty {
		t.Errorf("the spin scrolled to %d", a.sel)
	}
	a.View()
	// Up and down in the same frame cancel out.
	a.Update(tea.MouseWheelMsg{X: 3, Y: 3, Button: tea.MouseWheelUp})
	a.Update(tea.MouseWheelMsg{X: 3, Y: 3, Button: tea.MouseWheelDown})
	a.Update(wheelMsg{})
	if a.sel != 8 {
		t.Errorf("up then down moved to %d", a.sel)
	}
}

func TestAppNavLooksAndWraps(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	// Up from the top comes round to the bottom, and down from there back.
	key(a, "k")
	if a.selected().kind != entryLog {
		t.Errorf("up from Next selected %+v", a.selected())
	}
	key(a, "j")
	if a.selected().kind != entryNext {
		t.Errorf("down from the bottom selected %+v", a.selected())
	}
	// The selected entry has a green bar down its left, not inverse video.
	out := a.render()
	bar := tui.Color("▌", tui.Accent)
	if !strings.Contains(out, bar+tui.SGR(1)+"⏩ Next") || strings.Contains(out, tui.SGR(7)) {
		t.Errorf("the selected entry:\n%q", out)
	}
	// The intake box's rule joins the border, green beside the box.
	key(a, "i")
	lines := strings.Split(ansi.Strip(a.render()), "\n")
	if row := lines[a.labelRow]; !strings.Contains(row, "─┤") {
		t.Errorf("the intake rule = %q", row)
	}
	if !strings.Contains(strings.Split(a.render(), "\n")[a.labelRow], tui.Color("┤", tui.Accent)) {
		t.Error("the border beside the intake box isn't green")
	}
}
