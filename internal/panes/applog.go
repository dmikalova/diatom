package panes

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// logTail is how much of the end of the log the window reads.
const logTail = 256 << 10

// LogPath is where the scheduler logs: in the repo's .diatom/.
func LogPath(s *queue.Store) string { return filepath.Join(s.Root, "diatom.log") }

// logLines are the end of the scheduler's log, wrapped to w, errors in red
// and warnings in yellow.
func logLines(path string, w int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{tui.Dim("Nothing logged yet.")}
	}
	defer func() { _ = f.Close() }()
	cut := false
	if fi, err := f.Stat(); err == nil && fi.Size() > logTail {
		_, _ = f.Seek(fi.Size()-logTail, io.SeekStart)
		cut = true
	}
	b, _ := io.ReadAll(f)
	raw := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if cut && len(raw) > 1 {
		// The first line starts partway.
		raw = raw[1:]
	}
	var out []string
	for _, l := range raw {
		c := tui.NoColor
		switch {
		case strings.Contains(l, "level=ERROR"):
			c = tui.Red
		case strings.Contains(l, "level=WARN"):
			c = tui.Yellow
		}
		for wl := range strings.SplitSeq(ansi.Wordwrap(l, max(w, 20), ""), "\n") {
			out = append(out, tui.Color(wl, c))
		}
	}
	return out
}

// renderLog shows the newest of the scheduler's log that fits, scrolled back
// logBack lines.
func (a *App) renderLog(w, h int) string {
	lines := logLines(LogPath(a.env.Store), w)
	room := max(h-2, 1)
	a.logBack = min(a.logBack, max(len(lines)-room, 0))
	end := len(lines) - a.logBack
	head := tui.Bold("Scheduler log") + tui.Dim(" · k and j scroll · esc closes")
	return head + "\n\n" + strings.Join(lines[max(end-room, 0):end], "\n")
}

// logKey scrolls the log, or closes it.
func (a *App) logKey(key string) {
	room := max(a.height-3, 1)
	switch key {
	case "esc", "left", "L":
		a.logOpen, a.logBack = false, 0
	case "k", "up":
		a.logBack++
	case "j", "down":
		a.logBack = max(a.logBack-1, 0)
	case "pgup":
		a.logBack += room
	case "pgdown", "space", " ":
		a.logBack = max(a.logBack-room, 0)
	case "G", "end":
		a.logBack = 0
	}
}
