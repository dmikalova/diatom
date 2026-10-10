package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
	"github.com/dmikalova/diatom/internal/workspace"
)

// finishedGoal is a goal whose life is over, landed upstream or dropped,
// when it ended and what its sessions cost.
type finishedGoal struct {
	goal *queue.Goal
	// repo is the goal's, named only when the window is over several.
	repo string
	at   time.Time
	cost float64
}

// loadFinished reads the goals that are over, across the workspace's repos,
// the latest first.
func (a *App) loadFinished() []finishedGoal {
	var out []finishedGoal
	for _, store := range a.env.stores() {
		goals, err := store.Goals()
		if err != nil {
			return a.finished
		}
		for _, g := range goals {
			if !g.Over() {
				continue
			}
			f := finishedGoal{goal: g, repo: workspace.Name(store), at: g.Finished}
			if f.at.IsZero() {
				// A goal finished before diatom kept the time: its file was
				// last written as it finished.
				if fi, err := os.Stat(
					filepath.Join(store.GoalDir(g.Name), "goal.yaml"),
				); err == nil {
					f.at = fi.ModTime()
				}
			}
			f.cost, _ = a.status.goalCost(store, g.Name)
			out = append(out, f)
		}
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
		if a.env.many() {
			meta = []string{f.repo + "/" + g.Name}
		}
		if !f.at.IsZero() {
			word := "finished "
			if g.State == queue.GoalDropped {
				word = "dropped "
			}
			meta = append(meta, word+f.at.Local().Format("Jan 2 15:04"))
		}
		if f.cost > 0 {
			meta = append(meta, money(f.cost))
		}
		lines = append(lines, tui.Bold(title), "  "+tui.Dim(strings.Join(meta, " · ")))
		if g.Reason != "" {
			for _, l := range hang("Dropped: "+g.Reason, w-2) {
				lines = append(lines, "  "+tui.Color(l, tui.Yellow))
			}
		}
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
		" · landed upstream or dropped, the latest first · k and j scroll · esc closes",
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
	case "pgup", "shift+space":
		a.finishedTop = max(a.finishedTop-room, 0)
	case "pgdown", "space", " ":
		a.finishedTop += room
	case "g", "home":
		a.finishedTop = 0
	}
	return false
}
