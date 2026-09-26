package reviewui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// Follow is the reviewer as a workspace pane: it reviews the focused goal and
// switches when the status pane moves the focus (ADR 0007).
type Follow struct {
	ctx   context.Context
	file  focus.File
	cur   focus.Focus
	inner *Model

	width, height int
	err           error
}

// NewFollow starts a reviewer that follows the focus.
func NewFollow(ctx context.Context, file focus.File) *Follow {
	f := &Follow{ctx: ctx, file: file, width: 100, height: 30}
	f.refocus()
	return f
}

// refocus reopens the reviewer when the focus has moved to another goal.
func (f *Follow) refocus() {
	fc, err := f.file.Read()
	if err != nil {
		f.err = err
		return
	}
	if fc == f.cur && (f.inner != nil || fc.Goal == "") {
		return
	}
	f.cur, f.inner, f.err = fc, nil, nil
	if fc.Goal == "" {
		return
	}
	m, err := New(f.ctx, queue.Open(fc.Repo), fc.Goal)
	if err != nil {
		f.err = err
		return
	}
	m.Update(tea.WindowSizeMsg{Width: f.width, Height: f.height})
	f.inner = m
}

// Init implements tea.Model.
func (f *Follow) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (f *Follow) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.width, f.height = msg.Width, msg.Height
	case tickMsg:
		if f.inner == nil || !f.inner.editing {
			f.refocus()
		}
		if f.inner != nil {
			f.inner.Update(msg) // its own reload; this tick is the only one
		}
		return f, tick()
	case tea.KeyPressMsg:
		if f.inner == nil {
			if s := msg.String(); s == "q" || s == "ctrl+c" {
				return f, tea.Quit
			}
			return f, nil
		}
	}
	if f.inner == nil {
		return f, nil
	}
	_, cmd := f.inner.Update(msg)
	return f, cmd
}

// View implements tea.Model.
func (f *Follow) View() tea.View {
	if f.inner != nil {
		return f.inner.View()
	}
	text := tui.Bold(
		"diatom review",
	) + "\n\nNo goal is focused. Pick one in the status pane with enter.\n"
	if f.err != nil {
		text += "\n" + tui.Color(f.err.Error(), tui.Red) + "\n"
	}
	v := tea.NewView(text)
	v.AltScreen = true
	return v
}
