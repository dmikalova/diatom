// Package plan is a goal's plan and how a goal gets one (ADR 0010): a new
// goal starts in planning with a grilling task, grilling ends by handing in a
// plan of workstreams and small tasks, and the human's sign-off turns the plan
// into the goal's workstreams and tasks and makes the goal active.
package plan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// Plan is what grilling hands in for the human to sign off.
type Plan struct {
	// Summary says in a few sentences what the goal will do and how.
	Summary     string             `yaml:"summary"`
	Workstreams []queue.Workstream `yaml:"workstreams"`
	Tasks       []Task             `yaml:"tasks"`
}

// Task is one planned task. Its key is local to the plan: other tasks name it
// in after, and sign-off maps it to the task's id.
type Task struct {
	Key        string   `yaml:"key"`
	Title      string   `yaml:"title"`
	Workstream string   `yaml:"workstream"`
	Profile    string   `yaml:"profile,omitempty"`
	After      []string `yaml:"after,omitempty"`
	Body       string   `yaml:"body,omitempty"`
}

// FileName is the plan's file in the goal's directory.
const FileName = "plan.yaml"

// Parse reads a plan and checks its shape. Profiles are checked by Validate,
// which needs the config.
func Parse(b []byte) (*Plan, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var p Plan
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	return &p, p.Validate(nil)
}

// Validate checks that the plan can be signed off: valid and unique names,
// dependencies that exist and don't loop, and known profiles when profiles is
// given.
func (p *Plan) Validate(profiles map[string]config.Profile) error {
	if len(p.Workstreams) == 0 || len(p.Tasks) == 0 {
		return errors.New("a plan needs at least one workstream and one task")
	}
	errs := append(p.validateWorkstreams(), p.validateTasks(profiles)...)
	if len(errs) == 0 {
		if _, err := p.order(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Plan) validateWorkstreams() []error {
	var errs []error
	names := map[string]bool{}
	for _, w := range p.Workstreams {
		if err := queue.ValidName(w.Name); err != nil {
			errs = append(errs, fmt.Errorf("workstream: %w", err))
		}
		if names[w.Name] {
			errs = append(errs, fmt.Errorf("workstream %s is listed twice", w.Name))
		}
		names[w.Name] = true
	}
	for _, w := range p.Workstreams {
		for _, d := range w.DependsOn {
			if !names[d] || d == w.Name {
				errs = append(
					errs,
					fmt.Errorf("workstream %s depends on %q, which is not another workstream "+
						"of the plan", w.Name, d),
				)
			}
		}
	}
	return errs
}

func (p *Plan) validateTasks(profiles map[string]config.Profile) []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	keys := map[string]bool{}
	for _, t := range p.Tasks {
		switch {
		case t.Key == "":
			bad("task %q has no key", t.Title)
		case keys[t.Key]:
			bad("task key %s is used twice", t.Key)
		}
		keys[t.Key] = true
		if strings.TrimSpace(t.Title) == "" {
			bad("task %s has no title", t.Key)
		}
		if !slices.ContainsFunc(
			p.Workstreams,
			func(w queue.Workstream) bool { return w.Name == t.Workstream },
		) {
			bad("task %s is on workstream %q, which the plan doesn't list", t.Key, t.Workstream)
		}
		if _, ok := profiles[t.Profile]; profiles != nil && t.Profile != "" && !ok {
			bad("task %s uses unknown profile %q", t.Key, t.Profile)
		}
	}
	for _, t := range p.Tasks {
		for _, a := range t.After {
			if !keys[a] || a == t.Key {
				bad("task %s comes after %q, which is not another task of the plan", t.Key, a)
			}
		}
	}
	return errs
}

// deps returns each task's dependencies: the tasks it names in after, and
// every task of the workstreams its own workstream depends on.
func (p *Plan) deps() map[string][]string {
	byWS := map[string][]string{}
	for _, t := range p.Tasks {
		byWS[t.Workstream] = append(byWS[t.Workstream], t.Key)
	}
	wsDeps := map[string][]string{}
	for _, w := range p.Workstreams {
		wsDeps[w.Name] = w.DependsOn
	}
	out := map[string][]string{}
	for _, t := range p.Tasks {
		d := slices.Clone(t.After)
		for _, w := range wsDeps[t.Workstream] {
			d = append(d, byWS[w]...)
		}
		out[t.Key] = d
	}
	return out
}

// order returns the tasks with every task after its dependencies, keeping the
// plan's own order where it can.
func (p *Plan) order() ([]Task, error) {
	deps := p.deps()
	done := map[string]bool{}
	var out []Task
	for len(out) < len(p.Tasks) {
		progressed := false
		for _, t := range p.Tasks {
			if done[t.Key] ||
				slices.ContainsFunc(deps[t.Key], func(d string) bool { return !done[d] }) {
				continue
			}
			done[t.Key], progressed = true, true
			out = append(out, t)
		}
		if !progressed {
			return nil, errors.New("the plan's dependencies form a loop")
		}
	}
	return out, nil
}

// Load reads a goal's plan, or returns nil when grilling hasn't handed one
// in.
func Load(goalDir string) (*Plan, error) {
	b, err := os.ReadFile(filepath.Join(goalDir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Save writes a goal's plan.
func Save(goalDir string, p *Plan) error {
	b, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	tmp := filepath.Join(goalDir, "."+FileName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(goalDir, FileName))
}

// Supersede sets a plan aside when the human asks for changes, so grilling
// starts its next round without it.
func Supersede(goalDir string, now time.Time) error {
	from := filepath.Join(goalDir, FileName)
	to := filepath.Join(goalDir, "plan."+now.UTC().Format("20060102T150405Z")+".superseded.yaml")
	err := os.Rename(from, to)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// DraftsDir is where grilling drafts a goal's ADRs.
func DraftsDir(goalDir string) string { return filepath.Join(goalDir, "adr") }

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// NewGoal creates a goal in planning with a grilling task holding what the
// human asked for (ADR 0010). An empty name is made from the title.
func NewGoal(
	ctx context.Context,
	s *queue.Store,
	name, title, body string,
	origin queue.Origin,
	now time.Time,
) (*queue.Goal, error) {
	title = strings.TrimSpace(title)
	if name == "" {
		name = uniqueName(s, slug(title))
	}
	base, err := git.Repo{Dir: s.Repo()}.CurrentBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"a goal branches from the repo's current branch, and there is none: %w",
			err,
		)
	}
	g := &queue.Goal{Name: name, Title: title, State: queue.GoalPlanning, Base: base, Created: now}
	if err := s.CreateGoal(g); err != nil {
		return nil, err
	}
	if err := s.AddTask(name, &queue.Task{
		Title:   "Grill the goal: " + title,
		Kind:    queue.Grilling,
		Origin:  origin,
		Created: now,
		Body:    body,
	}); err != nil {
		return nil, err
	}
	return g, nil
}

// slug makes a goal name from the first few words of a title.
func slug(title string) string {
	words := strings.Fields(slugRe.ReplaceAllString(strings.ToLower(title), " "))
	if len(words) > 5 {
		words = words[:5]
	}
	s := strings.Join(words, "-")
	if s == "" {
		return "goal"
	}
	return s
}

func uniqueName(s *queue.Store, base string) string {
	name := base
	for n := 2; ; n++ {
		if _, err := os.Stat(s.GoalDir(name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
}

// Approve signs a goal's plan off: the plan's workstreams join the goal, its
// tasks are queued in dependency order, and the goal becomes active. When the
// repo has an ADR directory, grilling's ADR drafts are committed there on the
// integration branch and reviewed like code (ADR 0010); otherwise they stay in
// the goal's directory.
func Approve(
	ctx context.Context,
	s *queue.Store,
	cfg *config.Config,
	goal string,
	now time.Time,
) error {
	g, err := s.Goal(goal)
	if err != nil {
		return err
	}
	if g.State != queue.GoalPlanning {
		return fmt.Errorf(
			"goal %s is %s; only a goal in planning has a plan to sign off",
			goal,
			g.State,
		)
	}
	p, err := Load(s.GoalDir(goal))
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("goal %s has no plan yet: grilling hands one in", goal)
	}
	if err := p.Validate(cfg.Profiles); err != nil {
		return err
	}
	ordered, err := p.order()
	if err != nil {
		return err
	}
	for _, w := range p.Workstreams {
		if _, ok := g.Workstream(w.Name); !ok {
			g.Workstreams = append(g.Workstreams, w)
		}
	}
	deps := p.deps()
	ids := map[string]string{}
	for i, t := range ordered {
		task := &queue.Task{
			Title:      t.Title,
			Kind:       queue.Planned,
			Profile:    t.Profile,
			Workstream: t.Workstream,
			Priority:   i,
			Origin:     queue.Origin{Type: "plan", Ref: t.Key},
			Created:    now,
			Body:       t.Body,
		}
		for _, d := range deps[t.Key] {
			task.DependsOn = append(task.DependsOn, ids[d])
		}
		if err := s.AddTask(goal, task); err != nil {
			return err
		}
		ids[t.Key] = task.ID
	}
	if err := commitDrafts(ctx, s, cfg, g); err != nil {
		return err
	}
	g.State = queue.GoalActive
	return s.SaveGoal(g)
}

// commitDrafts commits grilling's ADR drafts into the repo's ADR directory on
// the integration branch, and records the commit on the grilling task so it
// comes up for review.
func commitDrafts(ctx context.Context, s *queue.Store, cfg *config.Config, g *queue.Goal) error {
	if cfg.ADR.Dir == "" {
		return nil
	}
	drafts, err := filepath.Glob(filepath.Join(DraftsDir(s.GoalDir(g.Name)), "*.md"))
	if err != nil || len(drafts) == 0 {
		return err
	}
	files := map[string][]byte{}
	for _, d := range drafts {
		b, err := os.ReadFile(d)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(filepath.Join(cfg.ADR.Dir, filepath.Base(d)))] = b
	}
	repo := git.Repo{Dir: s.Repo()}
	if err := repo.CreateBranch(ctx, g.IntegrationBranch(), g.Base); err != nil {
		return err
	}
	sha, err := repo.CommitFiles(ctx, g.IntegrationBranch(), files,
		fmt.Sprintf("docs(adr): record the decisions of %s\n", g.Name))
	if err != nil {
		return err
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return err
	}
	for _, t := range slices.Backward(tasks) {
		if t.Kind == queue.Grilling {
			t.Commits = append(t.Commits, sha)
			return s.SaveTask(g.Name, t)
		}
	}
	return nil
}

// Describe renders a plan for the human to read before signing it off.
func Describe(p *Plan) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(p.Summary) + "\n\nWorkstreams:\n")
	for _, w := range p.Workstreams {
		b.WriteString("  " + w.Name)
		if len(w.DependsOn) > 0 {
			b.WriteString(" (after " + strings.Join(w.DependsOn, ", ") + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nTasks:\n")
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "  [%s] %s", t.Workstream, t.Title)
		if len(t.After) > 0 {
			b.WriteString(" (after " + strings.Join(t.After, ", ") + ")")
		}
		if t.Profile != "" {
			b.WriteString(" on " + t.Profile)
		}
		b.WriteString("\n")
	}
	return b.String()
}
