package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/tui"
)

func TestHangAll(t *testing.T) {
	long := "  ✓ 0001 Grill the goal: close the effect catalog behind a RulesBearing marker · $4.89"
	got := strings.Split(hangAll(long+"\nshort", 40), "\n")
	if len(got) < 3 || !strings.HasPrefix(got[0], "  ✓ 0001") || got[len(got)-1] != "short" {
		t.Fatalf("wrapped = %q", got)
	}
	for _, l := range got[1 : len(got)-1] {
		// Under the text after the ✓, a hang in.
		if !strings.HasPrefix(l, "      ") || strings.HasPrefix(l, "       ") ||
			ansi.StringWidth(l) > 40 {
			t.Errorf("a later line = %q", l)
		}
	}
	// A focus bar goes down the later lines too, and a style carries on.
	barred := tui.Color("▌ ", tui.Accent) + tui.Dim(strings.Repeat("word ", 20))
	lines := strings.Split(hangAll(barred, 30), "\n")
	if len(lines) < 2 || !strings.HasPrefix(ansi.Strip(lines[1]), "▌   word") ||
		!strings.Contains(lines[1], tui.SGR(tui.FG(tui.Gray))) {
		t.Errorf("barred = %q", lines)
	}
	// A cursor marks only the first line.
	if got := strings.Split(
		ansi.Strip(hangAll("› "+strings.Repeat("word ", 12), 30)),
		"\n",
	); len(
		got,
	) < 2 ||
		!strings.HasPrefix(got[1], "    word") {
		t.Errorf("cursor = %q", got)
	}
	// Too narrow to hang, it is left for the pane.
	if got := hangAll(strings.Repeat("x", 30), 11); got != strings.Repeat("x", 30) {
		t.Errorf("narrow = %q", got)
	}
}
