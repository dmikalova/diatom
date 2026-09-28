package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/tui"
)

// selection is text the human is selecting in the main pane by dragging,
// from the cell they pressed on to the one under the pointer, in the pane's
// own columns and rows. The window holds the mouse, so the terminal's own
// selection would run across the nav too; this one keeps to the pane.
type selection struct {
	// dragging is set while the button is held, and on once the pointer
	// has moved off the cell pressed.
	dragging, on           bool
	fromX, fromY, toX, toY int
}

// mainLeft is the first column of the main pane.
func (a *App) mainLeft() int {
	if nw := a.nw(); nw > 0 {
		return nw + 1
	}
	return 0
}

// startSelect anchors a selection at a press in the main pane.
func (a *App) startSelect(m tea.Mouse) {
	x := m.X - a.mainLeft()
	a.selecting = selection{dragging: true, fromX: x, fromY: m.Y, toX: x, toY: m.Y}
}

// extendSelect moves the selection's end to the pointer, held to the pane.
func (a *App) extendSelect(m tea.Mouse) {
	a.selecting.toX = max(min(m.X-a.mainLeft(), a.mainWidth()-1), 0)
	a.selecting.toY = max(min(m.Y, a.height-1), 0)
	a.selecting.on = a.selecting.toX != a.selecting.fromX || a.selecting.toY != a.selecting.fromY
}

// endSelect copies what was selected once the button is let go, as the
// terminal's copy-on-select does.
func (a *App) endSelect() tea.Cmd {
	a.selecting.dragging = false
	if !a.selecting.on {
		return nil
	}
	text := a.selectedText()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	a.flash = fmt.Sprintf("copied %d characters", len([]rune(text)))
	return tea.SetClipboard(text)
}

// span is the selection in reading order, the start first, and whether it
// covers row y, with the columns it covers there, to past its last.
func (a *App) span(y int) (from, to int, ok bool) {
	x0, y0, x1, y1 := a.selecting.fromX, a.selecting.fromY, a.selecting.toX, a.selecting.toY
	if y1 < y0 || y1 == y0 && x1 < x0 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	if !a.selecting.on || y < y0 || y > y1 {
		return 0, 0, false
	}
	from, to = 0, a.mainWidth()
	if y == y0 {
		from = x0
	}
	if y == y1 {
		to = x1 + 1
	}
	return from, to, from < to
}

// selectedText is the selection's text, each row's trailing blanks dropped.
func (a *App) selectedText() string {
	var rows []string
	for y, line := range a.mainLines {
		if from, to, ok := a.span(y); ok {
			rows = append(rows, strings.TrimRight(ansi.Strip(ansi.Cut(line, from, to)), " "))
		}
	}
	return strings.Join(rows, "\n")
}

// highlight shows the selection over the main pane's lines, in reverse.
func (a *App) highlight(lines []string) []string {
	if !a.selecting.on {
		return lines
	}
	out := make([]string, len(lines))
	for y, line := range lines {
		out[y] = line
		from, to, ok := a.span(y)
		if !ok {
			continue
		}
		w := ansi.StringWidth(line)
		out[y] = ansi.Cut(line, 0, from) + tui.Reset + tui.SGR(7) +
			ansi.Strip(ansi.Cut(line, from, to)) + tui.Reset + ansi.Cut(line, to, w)
	}
	return out
}
