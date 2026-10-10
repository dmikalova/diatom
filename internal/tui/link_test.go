package tui

import "testing"

func TestLinkifyMarksBareURLs(t *testing.T) {
	const u = "https://linear.app/goodship/issue/DIP-4161"
	for _, c := range []struct{ in, want string }{
		{"see " + u + " now", "see " + Link(u, u) + " now"},
		{"see " + u + ".", "see " + Link(u, u) + "."},
		{"(" + u + ")", "(" + Link(u, u) + ")"},
		{"nothing to link", "nothing to link"},
		{Color(u, Green), SGR(FG(Green)) + Link(u, u) + Reset},
	} {
		if got := Linkify(c.in); got != c.want {
			t.Errorf("Linkify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
