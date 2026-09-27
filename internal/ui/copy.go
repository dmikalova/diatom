package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// copyFocused copies the text of what has the keyboard to the clipboard.
func (a *App) copyFocused() tea.Cmd {
	text, what := a.focusedText()
	if strings.TrimSpace(text) == "" {
		a.flash = "nothing to copy"
		return nil
	}
	a.flash = "copied " + what
	return tea.SetClipboard(text)
}

// focusedText is the text of what has the keyboard, and what it is: the
// nav's line, the part of Next's item, the text being written, or the main
// pane as it shows.
func (a *App) focusedText() (text, what string) {
	switch a.focus {
	case partIntake:
		return a.intake.area.Value(), "the intake"
	case partNav:
		return ansi.Strip(strings.TrimSpace(strings.Join(a.navEntry(-1, a.selected()), "\n"))),
			"the nav's line"
	}
	it := a.next.shown()
	if a.selected().kind != entryNext || a.logOpen || it == nil {
		return ansi.Strip(a.renderMain()), "the main pane"
	}
	switch a.next.area {
	case areaContext:
		return ansi.Strip(strings.Join(a.next.contextLines(*it, 1<<16), "\n")), "what it is about"
	case areaAnswer:
		if a.next.typing() {
			return a.next.answer.Value(), "the answer"
		}
	}
	if rv := a.next.reviews[it.row.goal.Name]; it.kind == itemReview && rv != nil {
		return ansi.Strip(rv.Render()), "the hunk"
	}
	return a.next.bodyText(*it), "the item"
}
