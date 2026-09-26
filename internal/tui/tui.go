// Package tui holds what diatom's terminal panes share: styling with the
// terminal's own 16 ANSI colors, so every pane follows the terminal's theme.
package tui

import (
	"strconv"
	"strings"
)

// The 16-color palette's indexes diatom uses; NoColor keeps the default.
const (
	NoColor = -1
	Red     = 1
	Green   = 2
	Yellow  = 3
	Blue    = 4
	Magenta = 5
	Cyan    = 6
	Gray    = 8
)

// Reset clears every attribute.
const Reset = "\x1b[0m"

// SGR returns an ANSI select-graphic-rendition sequence.
func SGR(codes ...int) string {
	s := make([]string, len(codes))
	for i, c := range codes {
		s[i] = strconv.Itoa(c)
	}
	return "\x1b[" + strings.Join(s, ";") + "m"
}

// FG is the SGR code for one of the 16 colors as foreground.
func FG(c int) int {
	if c >= 8 {
		return 90 + c - 8
	}
	return 30 + c
}

// BG is the SGR code for one of the 16 colors as background.
func BG(c int) int {
	if c >= 8 {
		return 100 + c - 8
	}
	return 40 + c
}

// Color renders s in color c.
func Color(s string, c int) string {
	if c == NoColor {
		return s
	}
	return SGR(FG(c)) + s + Reset
}

// Dim renders s in the muted gray.
func Dim(s string) string { return Color(s, Gray) }

// Bold renders s bold.
func Bold(s string) string { return SGR(1) + s + Reset }

// Short abbreviates a commit SHA.
func Short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
