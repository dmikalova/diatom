// Package roster describes a repo's goals to its agents. Every session's
// prompt lists the other goals, and `diatom task goals` details one, so an
// agent learns where other work stands without reading diatom's state.
package roster

import (
	"fmt"
	"io"
	"strings"

	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
)

// descriptionWidth caps a description made up for a goal that has none.
const descriptionWidth = 160

// Brief is one goal as another goal's agent sees it.
type Brief struct {
	Name, Title, Description string
	State                    queue.GoalState
	// After names the goals it waits for.
	After []string
	// Done of Total tasks are done.
	Done, Total int
}

// Briefs describes the repo's goals that aren't finished.
func Briefs(s *queue.Store) ([]Brief, error) {
	goals, err := s.Goals()
	if err != nil {
		return nil, err
	}
	var out []Brief
	for _, g := range goals {
		if g.State == queue.GoalFinished {
			continue
		}
		b := Brief{Name: g.Name, Title: g.Title, State: g.State, After: g.After}
		tasks, err := s.Tasks(g.Name)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			b.Total++
			if t.State == queue.Done {
				b.Done++
			}
		}
		if b.Description, err = description(s, g, tasks); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// Describe is what the goal is for, in a line.
func Describe(s *queue.Store, g *queue.Goal) (string, error) {
	if g.Description != "" {
		return g.Description, nil
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return "", err
	}
	return description(s, g, tasks)
}

// About is what the goal is for, as the human reads it: the first paragraph
// of its plan's summary, or of what it was started from before it has a
// plan, in full; or its description.
func About(s *queue.Store, g *queue.Goal) (string, error) {
	p, err := plan.Load(s.GoalDir(g.Name))
	if err != nil {
		return "", err
	}
	if p != nil {
		if para := paragraph(p.Summary); para != "" {
			return para, nil
		}
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return "", err
	}
	for _, t := range tasks {
		if para := paragraph(t.Body); t.Kind == queue.Grilling && para != "" {
			return para, nil
		}
	}
	return g.Description, nil
}

// paragraph is the first paragraph of text that isn't a heading, on one line.
func paragraph(text string) string {
	for p := range strings.SplitSeq(strings.TrimSpace(text), "\n\n") {
		if p = strings.TrimSpace(p); p != "" && !strings.HasPrefix(p, "#") {
			return strings.Join(strings.Fields(p), " ")
		}
	}
	return ""
}

// Clip is the first paragraph of text that isn't a heading, on one line and
// cut to a line's worth.
func Clip(text string) string { return clip(text) }

// description is the goal's own, or for a goal triage started before goals
// had one, the start of its plan's summary or of what it was started from.
func description(s *queue.Store, g *queue.Goal, tasks []*queue.Task) (string, error) {
	if g.Description != "" {
		return g.Description, nil
	}
	p, err := plan.Load(s.GoalDir(g.Name))
	if err != nil {
		return "", err
	}
	if p != nil && strings.TrimSpace(p.Summary) != "" {
		return clip(p.Summary), nil
	}
	for _, t := range tasks {
		if t.Kind == queue.Grilling {
			return clip(t.Body), nil
		}
	}
	return "", nil
}

// clip is the first paragraph of s that isn't a heading, on one line and cut
// to descriptionWidth.
func clip(s string) string {
	line := paragraph(s)
	if r := []rune(line); len(r) > descriptionWidth {
		line = strings.TrimRight(string(r[:descriptionWidth]), " ") + "…"
	}
	return line
}

// Line is the brief on one line of a list.
func (b Brief) Line() string {
	var l strings.Builder
	fmt.Fprintf(&l, "- `%s`: %s (%s", b.Name, b.Title, b.State)
	if b.Total > 0 {
		fmt.Fprintf(&l, ", %d of %d tasks done", b.Done, b.Total)
	}
	if len(b.After) > 0 {
		fmt.Fprintf(&l, "; waits for %s", strings.Join(b.After, ", "))
	}
	l.WriteString(")")
	if b.Description != "" {
		l.WriteString(". " + b.Description)
	}
	return l.String()
}

// Write lists the briefs, leaving out the goal named except.
func Write(w io.Writer, briefs []Brief, except string) error {
	for _, b := range briefs {
		if b.Name == except {
			continue
		}
		if _, err := fmt.Fprintln(w, b.Line()); err != nil {
			return err
		}
	}
	return nil
}

// Detail writes all an agent may know of one goal: its brief, plan summary,
// workstreams, tasks and how many questions wait on the human.
func Detail(w io.Writer, s *queue.Store, name string) error {
	g, err := s.Goal(name)
	if err != nil {
		return err
	}
	briefs, err := Briefs(s)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, br := range briefs {
		if br.Name == name {
			b.WriteString(br.Line() + "\n")
		}
	}
	p, err := plan.Load(s.GoalDir(name))
	if err != nil {
		return err
	}
	if p != nil && strings.TrimSpace(p.Summary) != "" {
		b.WriteString("\n## Plan summary\n\n" + strings.TrimSpace(p.Summary) + "\n")
	}
	if len(g.Workstreams) > 0 {
		b.WriteString("\n## Workstreams\n\n")
		for _, ws := range g.Workstreams {
			fmt.Fprintf(&b, "- %s", ws.Name)
			if len(ws.DependsOn) > 0 {
				fmt.Fprintf(&b, " (after %s)", strings.Join(ws.DependsOn, ", "))
			}
			b.WriteString("\n")
		}
	}
	tasks, err := s.Tasks(name)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		b.WriteString("\n## Tasks\n\n")
		for _, t := range tasks {
			ws := ""
			if t.Workstream != "" {
				ws = " [" + t.Workstream + "]"
			}
			fmt.Fprintf(&b, "- %s %s%s: %s\n", t.ID, t.State, ws, t.Title)
		}
	}
	qs, err := s.Questions(name, queue.QuestionOpen)
	if err != nil {
		return err
	}
	if n := len(qs); n > 0 {
		fmt.Fprintf(&b, "\n%d question(s) wait on the human.\n", n)
	}
	_, err = io.WriteString(w, b.String())
	return err
}
