package reviewui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/bluekeyes/go-gitdiff/gitdiff"

	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/tui"
)

// headerLines and footerLines are the rows around the diff.
const (
	headerLines = 5
	footerLines = 2
)

func (m *Model) bodyHeight() int {
	return max(m.height-headerLines-footerLines-len(m.causes()), 3)
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "diatom review · " + m.goal
	return v
}

func (m *Model) render() string {
	var b strings.Builder
	b.WriteString(m.summary() + "\n")
	if m.cur < 0 {
		b.WriteString(
			"\nNothing left to review. New commits appear here as the agents make them.\n",
		)
		b.WriteString("\n" + m.footer())
		return b.String()
	}
	it := m.items[m.cur]
	fmt.Fprintf(&b, "%s%s%s %s\n", sgr(fgCode(yellow)), short(it.Commit), reset, it.Subject)
	b.WriteString(dim(m.taskLine(it)) + "\n")
	fmt.Fprintf(
		&b,
		"%s%s%s · hunk %d of %d · %s\n",
		sgr(1),
		it.Path,
		reset,
		it.Index,
		it.Of,
		m.state(it),
	)
	for _, c := range m.causes() {
		b.WriteString(c + "\n")
	}
	b.WriteString(dim(strings.Repeat("─", max(m.width, 1))) + "\n")
	b.WriteString(m.body())
	b.WriteString(m.footer())
	return b.String()
}

func (m *Model) summary() string {
	counts := review.Counts(m.items)
	pending := len(review.Pending(m.items))
	s := fmt.Sprintf("%sdiatom review%s · %s · %d to review", sgr(1), reset, m.goal, pending)
	if counts[review.Defer] > 0 {
		s += fmt.Sprintf(" (%d deferred)", counts[review.Defer])
	}
	s += fmt.Sprintf(" · %s✓ %d%s  %s✗ %d%s", sgr(fgCode(green)), counts[review.Approve], reset,
		sgr(fgCode(red)), counts[review.Reject], reset)
	if m.err != nil {
		s += "  " + sgr(fgCode(red)) + m.err.Error() + reset
	}
	return s
}

func (m *Model) taskLine(it review.Item) string {
	var parts []string
	for _, t := range it.Tasks {
		parts = append(parts, fmt.Sprintf("task %s: %s", t.ID, t.Title))
	}
	return strings.Join(parts, " · ")
}

func (m *Model) state(it review.Item) string {
	s := dim("unreviewed")
	if it.Record != nil {
		color := map[review.Decision]int{review.Approve: green, review.Reject: red, review.Defer: yellow}[it.Record.Decision]
		s = sgr(fgCode(color)) + string(it.Record.Decision) + reset
	}
	if it.Revision != nil && it.Revision.Revises != "" {
		s += " · fixup of " + short(it.Revision.Revises)
		if m.combined {
			s += " · " + sgr(fgCode(cyan)) + "combined view" + reset
		}
	}
	return s
}

// causes are the review comments that caused a fixup, shown above its diff
// (ADR 0001).
func (m *Model) causes() []string {
	if m.cur < 0 {
		return nil
	}
	rev := m.items[m.cur].Revision
	if rev == nil {
		return nil
	}
	var out []string
	for l := range strings.SplitSeq(rev.Body, "\n") {
		if strings.HasPrefix(l, "- Line ") || strings.HasPrefix(l, "- The human rejected") {
			out = append(out, sgr(fgCode(magenta))+"  ↳ "+strings.TrimPrefix(l, "- ")+reset)
		}
	}
	if len(out) > 0 {
		out = append([]string{dim("The review comments that caused this fixup:")}, out...)
	}
	return out
}

func (m *Model) body() string {
	ls := m.shown()
	width := max(m.width, 20)
	gutter := 1
	for _, l := range ls {
		gutter = max(gutter, len(strconv.Itoa(max(l.oldN, l.newN))))
	}
	comments := map[int]string{}
	if !m.combined {
		for _, c := range m.drafts {
			comments[c.Line] = c.Text
		}
	}
	var rows []string
	cursorRow := 0
	for i, l := range ls {
		if i == m.cursor {
			cursorRow = len(rows)
		}
		rows = append(rows, m.row(l, i == m.cursor, gutter, width))
		if text, ok := comments[i]; ok {
			rows = append(
				rows,
				sgr(fgCode(magenta))+strings.Repeat(" ", 2*gutter+4)+"💬 "+text+reset,
			)
		}
	}
	// Keep the cursor in view even with comment rows added.
	h := m.bodyHeight()
	start := min(m.scroll, max(len(rows)-h, 0))
	if cursorRow >= start+h {
		start = cursorRow - h + 1
	}
	if cursorRow < start {
		start = cursorRow
	}
	end := min(start+h, len(rows))
	var b strings.Builder
	for _, r := range rows[start:end] {
		b.WriteString(r + "\n")
	}
	for range h - (end - start) {
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Model) row(l line, cursor bool, gutter, width int) string {
	num := func(n int) string {
		if n == 0 {
			return strings.Repeat(" ", gutter)
		}
		return fmt.Sprintf("%*d", gutter, n)
	}
	mark := " "
	if cursor {
		mark = sgr(1, fgCode(cyan)) + "›" + reset
	}
	sign, signColor := " ", noColor
	switch l.op {
	case gitdiff.OpAdd:
		sign, signColor = "+", green
	case gitdiff.OpDelete:
		sign, signColor = "-", red
	}
	oldN, newN := l.oldN, l.newN
	if l.op == gitdiff.OpAdd {
		oldN = 0
	}
	if l.op == gitdiff.OpDelete {
		newN = 0
	}
	prefix := fmt.Sprintf(
		"%s%s %s %s",
		mark,
		dim(num(oldN)),
		dim(num(newN)),
		colored(sign, signColor),
	)
	return prefix + " " + renderCells(l.cells, l.op, width-2*gutter-5)
}

func (m *Model) footer() string {
	if m.editing {
		return m.input.View() + "\n"
	}
	keys := "a approve · r reject · d defer · c comment · x drop comment · n/p skip · u back · v combined · q quit"
	if m.flash != "" {
		return sgr(fgCode(cyan)) + m.flash + reset + "\n" + dim(keys)
	}
	return "\n" + dim(keys)
}

func dim(s string) string { return tui.Dim(s) }

func colored(s string, c int) string { return tui.Color(s, c) }

func short(sha string) string { return tui.Short(sha) }
