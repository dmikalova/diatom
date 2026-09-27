package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// finishedGoal is a goal landed upstream, when it was found landed, and what
// its sessions cost.
type finishedGoal struct {
	goal *queue.Goal
	at   time.Time
	cost float64
}

// loadFinished reads the finished goals, the latest finished first.
func (a *App) loadFinished() []finishedGoal {
	goals, err := a.env.Store.Goals()
	if err != nil {
		return a.finished
	}
	var out []finishedGoal
	for _, g := range goals {
		if g.State != queue.GoalFinished {
			continue
		}
		f := finishedGoal{goal: g, at: g.Finished}
		if f.at.IsZero() {
			// A goal finished before diatom kept the time: its file was
			// last written as it finished.
			if fi, err := os.Stat(
				filepath.Join(a.env.Store.GoalDir(g.Name), "goal.yaml"),
			); err == nil {
				f.at = fi.ModTime()
			}
		}
		f.cost, _ = a.status.goalCost(g.Name)
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(x, y finishedGoal) int { return y.at.Compare(x.at) })
	return out
}

// renderFinished lists the finished goals, the latest first, each with when
// it finished, what it cost and what it was for, scrolled down finishedTop
// lines.
func (a *App) renderFinished(w, h int) string {
	var lines []string
	for _, f := range a.finished {
		g := f.goal
		title := g.Title
		if title == "" {
			title = g.Name
		}
		meta := []string{g.Name}
		if !f.at.IsZero() {
			meta = append(meta, "finished "+f.at.Local().Format("Jan 2 15:04"))
		}
		if f.cost > 0 {
			meta = append(meta, money(f.cost))
		}
		lines = append(lines, tui.Bold(title), "  "+tui.Dim(strings.Join(meta, " · ")))
		if g.Description != "" {
			for _, l := range hang(g.Description, w-2) {
				lines = append(lines, "  "+l)
			}
		}
		lines = append(lines, "")
	}
	room := max(h-2, 1)
	a.finishedTop = max(min(a.finishedTop, len(lines)-room), 0)
	head := tui.Bold(
		"Finished",
	) + tui.Dim(
		" · landed upstream, the latest first · k and j scroll · esc closes",
	)
	return head + "\n\n" + strings.Join(
		lines[a.finishedTop:min(a.finishedTop+room, len(lines))],
		"\n",
	)
}

// finishedKey scrolls the finished goals, and reports whether the key goes
// back to the nav.
func (a *App) finishedKey(key string) bool {
	room := max(a.height-3, 1)
	switch key {
	case "esc", "left":
		return true
	case "k", "up":
		a.finishedTop = max(a.finishedTop-1, 0)
	case "j", "down":
		a.finishedTop++
	case "pgup":
		a.finishedTop = max(a.finishedTop-room, 0)
	case "pgdown", "space", " ":
		a.finishedTop += room
	case "g", "home":
		a.finishedTop = 0
	}
	return false
}
