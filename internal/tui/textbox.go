package tui

import (
	"strconv"

	keybind "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TextBox is a text box as the window's are: no line numbers or prompt, the
// terminal's own colors, and the window's editing keys, enter left to the
// box's owner.
func TextBox() textarea.Model {
	area := textarea.New()
	area.ShowLineNumbers = false
	area.Prompt = ""
	EditKeys(&area)
	area.SetStyles(PlainStyles())
	return area
}

// PlainStyles styles a text box in the terminal's own colors, as the rest of
// the window is, rather than for a dark background: plain text, and the
// placeholder in the window's gray.
func PlainStyles() textarea.Styles {
	s := textarea.DefaultDarkStyles()
	plain := textarea.StyleState{
		Base:        lipgloss.NewStyle(),
		Text:        lipgloss.NewStyle(),
		CursorLine:  lipgloss.NewStyle(),
		EndOfBuffer: lipgloss.NewStyle(),
		Prompt:      lipgloss.NewStyle(),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(Gray))),
		Selection:   lipgloss.NewStyle().Reverse(true),
	}
	s.Focused, s.Blurred = plain, plain
	s.Cursor.Color = lipgloss.Color(strconv.Itoa(Accent))
	return s
}

// The keys the text boxes edit with, beside the text box's own. The shifted
// ones need a terminal that tells them apart, as Ghostty does; the alt ones
// work in any.
var (
	// newline adds a line to the text instead of sending it.
	newline   = keybind.NewBinding(keybind.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	selectAll = keybind.NewBinding(keybind.WithKeys("alt+a", "ctrl+shift+a"))
	copyText  = keybind.NewBinding(keybind.WithKeys("ctrl+shift+c", "alt+c"))
	cutText   = keybind.NewBinding(keybind.WithKeys("ctrl+x", "alt+x"))
)

// EditKeys gives a text box the window's editing keys. Pasting, with the
// terminal's paste or ctrl+v, and selecting with shift and the arrows are the
// text box's own.
func EditKeys(area *textarea.Model) {
	area.KeyMap.InsertNewline = newline
	area.KeyMap.SelectAll = selectAll
	area.KeyMap.CopySelection = copyText
}

// Cut copies the selected text and deletes it, which the text box has no key
// for.
func Cut(area *textarea.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if !keybind.Matches(msg, cutText) || !area.HasSelection() {
		return nil, false
	}
	cmd := area.CopySelection()
	area.DeleteSelection()
	return cmd, true
}
