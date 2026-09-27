package tui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// HangIndent is how far a wrapped line's later lines sit in from where its
// text starts, so they read as part of it.
const HangIndent = "  "

// Hang wraps a line to w under its lead, the spaces and bars it starts with:
// its later lines repeat the lead, a cursor blanked, then sit HangIndent in,
// keeping the line's styles. A line that fits, or too narrow a w, is left as
// it is.
func Hang(l string, w int) []string {
	if ansi.StringWidth(l) <= w {
		return []string{l}
	}
	plain := ansi.Strip(l)
	text := strings.TrimLeft(plain, " ▌│›")
	lead := ansi.StringWidth(plain[:len(plain)-len(text)])
	// A line that starts with an icon, such as a task's ✓, hangs under the
	// text after it.
	icon := 0
	if tok, _, ok := strings.Cut(text, " "); ok && tok != "" && ansi.StringWidth(tok) <= 2 &&
		!strings.ContainsFunc(
			tok,
			func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) },
		) {
		icon = ansi.StringWidth(tok) + 1
	}
	room := w - lead - icon - len(HangIndent)
	if room < 10 {
		return []string{l}
	}
	head, rest := ansi.Cut(l, 0, lead), ansi.Cut(l, lead, ansi.StringWidth(l))
	wrapped := strings.Split(lipgloss.Wrap(rest, room, ""), "\n")
	// The later lines keep a bar, but not a cursor, and step past the icon.
	under := strings.ReplaceAll(head, "›", " ") + strings.Repeat(" ", icon)
	out := make([]string, 0, len(wrapped))
	for i, part := range wrapped {
		if i == 0 {
			out = append(out, head+part)
			continue
		}
		out = append(out, under+HangIndent+part)
	}
	return out
}
