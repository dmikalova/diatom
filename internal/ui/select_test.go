package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/tui"
)

func TestSelectInTheMainPane(t *testing.T) {
	f := newFixture(t)
	a, _ := newApp(t, f)
	a.render()
	left := a.mainLeft()
	// The question of Next's item, on some row of the main pane.
	row := -1
	for y, l := range a.mainLines {
		if strings.Contains(ansi.Strip(l), "Does ward stack?") {
			row = y
		}
	}
	if row < 0 {
		t.Fatalf("no question on screen:\n%s", plain(a.render()))
	}
	col := strings.Index(ansi.Strip(a.mainLines[row]), "Does")
	press := func(x, y int) tea.Mouse { return tea.Mouse{X: left + x, Y: y, Button: tea.MouseLeft} }
	a.Update(tea.MouseClickMsg(press(col, row)))
	a.Update(tea.MouseMotionMsg(press(col+3, row+1)))
	if !a.selecting.on {
		t.Fatal("dragging selected nothing")
	}
	if out := a.render(); !strings.Contains(out, tui.SGR(7)) {
		t.Error("the selection isn't shown")
	}
	_, cmd := a.Update(tea.MouseReleaseMsg(press(col+3, row+1)))
	if cmd == nil || !strings.HasPrefix(a.selectedText(), "Does ward stack?\n") ||
		!strings.Contains(a.flash, "copied") {
		t.Errorf("released: %q, flash %q", a.selectedText(), a.flash)
	}
	// Dragged backwards, it reads the same way.
	a.Update(tea.MouseClickMsg(press(col+3, row)))
	a.Update(tea.MouseMotionMsg(press(col, row)))
	if got := a.selectedText(); got != "Does" {
		t.Errorf("backwards = %q", got)
	}
	// y copies the selection, and any other key lets it go.
	if a.copyFocused() == nil || !strings.Contains(a.flash, "the selection") {
		t.Errorf("y = %q", a.flash)
	}
	key(a, "j")
	if a.selecting.on {
		t.Error("a key kept the selection")
	}
	// A press without a drag selects nothing, and copies nothing.
	a.Update(tea.MouseClickMsg(press(col, row)))
	if _, cmd := a.Update(tea.MouseReleaseMsg(press(col, row))); cmd != nil || a.selecting.on {
		t.Error("a click selected")
	}
	// The wheel lets a selection go too.
	a.Update(tea.MouseClickMsg(press(col, row)))
	a.Update(tea.MouseMotionMsg(press(col+2, row)))
	a.Update(tea.MouseWheelMsg{X: left + 1, Y: row, Button: tea.MouseWheelDown})
	if a.selecting.on {
		t.Error("the wheel kept the selection")
	}
}
