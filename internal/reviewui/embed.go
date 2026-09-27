package reviewui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/review"
)

// SetSize fits the reviewer to its part of the window.
func (m *Model) SetSize(width, height int) {
	m.width, m.height = width, height
	m.input.SetWidth(max(width-4, 10))
}

// Render is the reviewer as it shows now.
func (m *Model) Render() string { return m.render() }

// Refresh rereads the review, as the reviewer's own tick does.
func (m *Model) Refresh() {
	if m.editing {
		return
	}
	m.err = m.reload()
	if m.cur < 0 {
		m.show(m.firstPending(""))
	}
}

// Key handles a key the window passes on.
func (m *Model) Key(msg tea.KeyPressMsg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

// Pending counts the hunks left to review.
func (m *Model) Pending() int { return len(review.Pending(m.items)) }

// Goal is the goal under review.
func (m *Model) Goal() string { return m.goal }
