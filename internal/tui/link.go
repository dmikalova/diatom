package tui

import (
	"regexp"
	"strings"
)

// Link marks text as a hyperlink to url with OSC 8, which terminals such as
// Ghostty make clickable. The target rides along in the escape, so the link
// still works where the text is wrapped or cut.
func Link(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// A bare URL in rendered text, stopping at whitespace, at an escape, and at
// the brackets and quotes that tend to hold a URL rather than belong to it.
var urlPattern = regexp.MustCompile(`https?://[^\s\x1b<>"'` + "`" + `()\[\]]+`)

// Trailing punctuation that ends the sentence, not the URL.
const urlTrailers = ".,;:!?·"

// Linkify makes every bare URL in s a Link to itself.
func Linkify(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	return urlPattern.ReplaceAllStringFunc(s, func(u string) string {
		tail := ""
		if trimmed := strings.TrimRight(u, urlTrailers); trimmed != u {
			u, tail = trimmed, u[len(trimmed):]
		}
		return Link(u, u) + tail
	})
}
