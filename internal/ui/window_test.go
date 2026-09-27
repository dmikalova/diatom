package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestAppHidesAndResizesTheNav(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "h")
	out := ansi.Strip(a.render())
	if strings.Contains(out, "»  Next") || a.focus != partMain {
		t.Errorf("h left the nav up, focus %d:\n%s", a.focus, out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != 120 {
			t.Fatalf("line %d is %d wide without the nav", i, w)
		}
	}
	// Going to the intake box shows the nav again.
	key(a, "i")
	if a.navHidden {
		t.Error("the intake box is hidden with the nav")
	}
	key(a, "esc")

	a.Update(tea.MouseClickMsg{X: navDefault, Y: 5, Button: tea.MouseLeft})
	a.Update(tea.MouseMotionMsg{X: 44, Y: 5, Button: tea.MouseLeft})
	a.Update(tea.MouseReleaseMsg{X: 44, Y: 5, Button: tea.MouseLeft})
	if a.nw() != 44 || a.dragging {
		t.Errorf("dragged to %d, dragging %v", a.nw(), a.dragging)
	}
	a.Update(tea.MouseMotionMsg{X: 100, Y: 5})
	if a.nw() != 44 {
		t.Errorf("moving without holding the edge resized to %d", a.nw())
	}
	// The width is kept for the next window.
	again, _ := newApp(t, f)
	if again.nw() != 44 {
		t.Errorf("a new window's nav is %d wide", again.nw())
	}
}

func TestAppShowsTheLog(t *testing.T) {
	f := newFixture(t)
	write(
		t,
		LogPath(f.store),
		"time=1 level=INFO msg=starting\ntime=2 level=ERROR msg=\"planning failed\"\n",
	)
	a, _ := newApp(t, f)
	key(a, "L")
	if out := ansi.Strip(a.render()); !strings.Contains(out, "Scheduler log") ||
		!strings.Contains(out, `msg="planning failed"`) || a.focus != partMain {
		t.Errorf("L shows:\n%s", out)
	}
	key(a, "esc")
	if a.logOpen || a.focus != partNav {
		t.Errorf("esc left the log open %v, focus %d", a.logOpen, a.focus)
	}
	// The footer opens it too.
	_, foot := a.navLines()
	a.Update(
		tea.MouseClickMsg{X: 3, Y: a.height - a.intakeHeight() - len(foot), Button: tea.MouseLeft},
	)
	if !a.logOpen {
		t.Error("clicking the footer doesn't open the log")
	}
}

func TestAppCopies(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	key(a, "enter")
	if cmd := a.copyFocused(); cmd == nil || a.flash != "copied the item" {
		t.Errorf("copying the question: %q", a.flash)
	}
	if text, _ := a.focusedText(); !strings.Contains(text, "It matters for poison.") {
		t.Errorf("copied %q", text)
	}
	key(a, "shift+tab")
	if text, what := a.focusedText(); what != "what it is about" || !strings.Contains(text,
		"Asked by task 0001: Add ward") {
		t.Errorf("copied %s: %q", what, text)
	}
	if _, cmd := a.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper}); cmd == nil {
		t.Error("cmd+c copies nothing")
	}
}

func TestAppNotifiesWhenSomethingArrives(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	a.Update(tea.BlurMsg{})
	if a.notify(); a.waiting == 0 {
		t.Fatal("nothing waits")
	}
	if cmd := a.notify(); cmd != nil {
		t.Error("it notified while something already waited")
	}
	a.waiting = 0
	if cmd := a.notify(); cmd == nil {
		t.Error("it didn't notify when something arrived")
	}
	a.waiting = 0
	a.Update(tea.FocusMsg{})
	if cmd := a.notify(); cmd != nil {
		t.Error("it notified while the terminal had the keyboard")
	}
}

func TestAppRestartsOnAnUpdate(t *testing.T) {
	f := newFixture(t)
	a, stopped := newApp(t, f)
	a.Update(UpdateMsg{Bin: "/bin/diatom", Why: "a new build is installed"})
	if foot := ansi.Strip(
		a.footer(),
	); !strings.Contains(
		foot,
		"a new build is installed · U restarts",
	) {
		t.Errorf("the footer doesn't say so: %q", foot)
	}
	key(a, "U")
	if !*stopped || a.Restart() != "/bin/diatom" {
		t.Errorf("U: stopped %v, restart %q", *stopped, a.Restart())
	}
}

func TestAppReviews(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	// The goal's page offers its review first.
	key(a, "j", "j", "enter")
	if acts := a.status.detailActions(); len(acts) == 0 || acts[0].key != "r" {
		t.Fatalf("actions = %+v", acts)
	}
	key(a, "enter")
	if a.review == nil || !strings.Contains(ansi.Strip(a.render()), "ward.go") {
		t.Fatalf("the review isn't open:\n%s", ansi.Strip(a.render()))
	}
	key(a, "esc")
	if a.review != nil || a.status.detail == nil {
		t.Error("esc from the review doesn't go back to the goal")
	}

	// Next reviews too, once the question is answered.
	key(a, "esc", "k", "k", "enter", "tab")
	typeText(a, "No")
	key(a, "enter")
	if it := a.next.shown(); it == nil || it.kind != itemReview {
		t.Fatalf("Next shows %+v", it)
	}
	if out := ansi.Strip(a.render()); !strings.Contains(out, "ward.go") ||
		!strings.Contains(out, "a approve") {
		t.Errorf("Next's review:\n%s", out)
	}
	key(a, "a")
	if !strings.Contains(ansi.Strip(a.render()), "Nothing needs you") {
		t.Errorf("after approving the last hunk:\n%s", ansi.Strip(a.render()))
	}
}

func TestViewerRestartsWithoutWaiting(t *testing.T) {
	f := newFixture(t)
	a := NewApp(context.Background(), f.env, Scheduler{Viewer: "elsewhere"})
	a.Update(UpdateMsg{Bin: "/bin/diatom", Why: "a new release is installed"})
	if _, cmd := a.Update(tea.KeyPressMsg{Code: 'U', Text: "U"}); cmd == nil || a.Restart() == "" {
		t.Error("a viewer doesn't restart on U")
	}
}
