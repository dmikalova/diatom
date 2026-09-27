package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/tui"
)

// hangAll wraps every line of s wider than w, rather than leaving it to break
// at the pane's edge: its later lines sit under its first, a tui.HangIndent in
// from where its text starts, with the same lead, such as a focus bar, down
// their left. Lines that fit, as the diff's do, are left as they are.
func hangAll(s string, w int) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		if ansi.StringWidth(l) <= w {
			out = append(out, l)
			continue
		}
		out = append(out, tui.Hang(l, w)...)
	}
	return strings.Join(out, "\n")
}

// wrapLines wraps each of lines wider than w, and says where the lines
// first and last now start and end.
func wrapLines(lines []string, w, first, last int) (out []string, newFirst, newLast int) {
	newFirst, newLast = first, last
	for i, l := range lines {
		part := []string{l}
		if ansi.StringWidth(l) > w {
			part = tui.Hang(l, w)
		}
		if i == first {
			newFirst = len(out)
		}
		out = append(out, part...)
		if i == last {
			newLast = len(out) - 1
		}
	}
	return out, newFirst, newLast
}
