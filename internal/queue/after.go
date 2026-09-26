package queue

import (
	"fmt"
	"slices"
	"strings"
)

// SetAfter makes a goal wait for the goals named in after, replacing what it
// waited for; none clears it. Each must be another goal of the repo, and no
// goal may end up waiting on itself.
func (s *Store) SetAfter(goal string, after []string) error {
	g, err := s.Goal(goal)
	if err != nil {
		return err
	}
	goals, err := s.Goals()
	if err != nil {
		return err
	}
	byName := map[string]*Goal{}
	for _, o := range goals {
		byName[o.Name] = o
	}
	var clean []string
	for _, name := range after {
		switch {
		case name == goal:
			return fmt.Errorf("goal %s can't wait for itself", goal)
		case byName[name] == nil:
			return fmt.Errorf("there is no goal %s to wait for", name)
		case !slices.Contains(clean, name):
			clean = append(clean, name)
		}
	}
	byName[goal].After = clean
	if loop := findLoop(byName, goal); loop != nil {
		return fmt.Errorf("goals can't wait for each other in a loop: %s", joinArrows(loop))
	}
	g.After = clean
	return s.SaveGoal(g)
}

// findLoop returns a chain of goals that leads from start back to it, or nil.
func findLoop(byName map[string]*Goal, start string) []string {
	var walk func(name string, path []string) []string
	walk = func(name string, path []string) []string {
		g := byName[name]
		if g == nil {
			return nil
		}
		for _, next := range g.After {
			if next == start {
				return append(path, name, start)
			}
			if !slices.Contains(path, next) {
				if loop := walk(next, append(path, name)); loop != nil {
					return loop
				}
			}
		}
		return nil
	}
	return walk(start, nil)
}

func joinArrows(names []string) string {
	var out strings.Builder
	for i, n := range names {
		if i > 0 {
			out.WriteString(" → ")
		}
		out.WriteString(n)
	}
	return out.String()
}

// Waiting returns the goals a goal still waits for: those it is after that
// aren't finished yet. A goal that no longer exists isn't waited for.
func (s *Store) Waiting(g *Goal) []string {
	var waiting []string
	for _, name := range g.After {
		o, err := s.Goal(name)
		if err != nil {
			continue
		}
		if o.State != GoalFinished {
			waiting = append(waiting, name)
		}
	}
	return waiting
}
