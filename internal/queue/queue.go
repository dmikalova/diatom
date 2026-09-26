// Package queue is diatom's runtime state: goals, their tasks and the questions
// they raise, stored as plain files under `$REPO/.diatom/` (ADR 0002).
//
// Each task is one Markdown file with YAML frontmatter, and its state is the
// directory it sits in. State changes are atomic renames, so after a crash a
// task is in exactly one state. Only the harness moves task files: agents add
// notes to a task's body through the task tool, which runs harness code.
//
// The layout of one goal:
//
//	.diatom/goals/<goal>/
//	  goal.yaml                  the goal's state, branches and workstreams
//	  tasks/<state>/<id>.md      pending, active, blocked or done
//	  questions/<state>/<id>.md  open or closed
//	  sessions/<id>/             one agent session's spec, report and log
//	  worktrees/<workstream>/    one git worktree per workstream
package queue

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// GoalState is where a goal is in its life (ADR 0003).
type GoalState string

// The goal states.
const (
	GoalPlanning GoalState = "planning"
	GoalActive   GoalState = "active"
	GoalParked   GoalState = "parked"
	// GoalDone is a goal whose work is over, waiting to land upstream.
	GoalDone GoalState = "done"
	// GoalFinished is a done goal merged into its base branch upstream, with
	// the checks there passing.
	GoalFinished GoalState = "finished"
)

// Goal is a unit of intent submitted by the human.
type Goal struct {
	// Name is the goal's directory name and branch prefix. It is not stored
	// in goal.yaml.
	Name  string    `yaml:"-"`
	Title string    `yaml:"title"`
	State GoalState `yaml:"state"`
	// Pinned goals are picked before every other goal (ADR 0007).
	Pinned bool `yaml:"pinned,omitempty"`
	// Base is the branch the integration branch started from.
	Base        string       `yaml:"base"`
	Created     time.Time    `yaml:"created"`
	Workstreams []Workstream `yaml:"workstreams,omitempty"`
}

// Workstream is a named line of work within a goal, with its own branch and
// worktree.
type Workstream struct {
	Name      string   `yaml:"name"`
	DependsOn []string `yaml:"dependsOn,omitempty"`
}

// IntegrationBranch is the branch that collects all of a goal's work.
func (g *Goal) IntegrationBranch() string { return "diatom/" + g.Name + "/integration" }

// WorkstreamBranch is the branch one workstream commits on.
func (g *Goal) WorkstreamBranch(ws string) string { return "diatom/" + g.Name + "/ws/" + ws }

// Workstream returns the named workstream, or false.
func (g *Goal) Workstream(name string) (Workstream, bool) {
	i := slices.IndexFunc(g.Workstreams, func(w Workstream) bool { return w.Name == name })
	if i < 0 {
		return Workstream{}, false
	}
	return g.Workstreams[i], true
}

// State is a task's state, which is the directory its file sits in.
type State string

// The task states.
const (
	Pending State = "pending"
	Active  State = "active"
	Blocked State = "blocked"
	Done    State = "done"
)

var taskStates = []State{Pending, Active, Blocked, Done}

// Kind is what a task is for. It sets the task's priority (ADR 0004) and its
// default profile (ADR 0006).
type Kind string

// The task kinds.
const (
	GateRepair Kind = "gate-repair"
	Conflict   Kind = "conflict"
	Revision   Kind = "revision"
	Triage     Kind = "triage"
	Grilling   Kind = "grilling"
	Planned    Kind = "planned"
)

// DefaultProfile is the profile a task of this kind runs on unless the
// planner chose another.
func (k Kind) DefaultProfile() string {
	switch k {
	case GateRepair, Conflict:
		return "mechanical"
	case Triage, Grilling:
		return "planning"
	default:
		return "implementation"
	}
}

// Task is one unit of agent work within a workstream.
type Task struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Kind       Kind     `yaml:"kind"`
	Profile    string   `yaml:"profile"`
	Workstream string   `yaml:"workstream,omitempty"`
	DependsOn  []string `yaml:"dependsOn,omitempty"`
	// Priority orders tasks of the same kind; lower runs first.
	Priority int    `yaml:"priority,omitempty"`
	Origin   Origin `yaml:"origin"`
	// Attempts counts the sessions that ended without finishing the task.
	Attempts int `yaml:"attempts,omitempty"`
	// Escalated is set once the task has been retried with more effort
	// (ADR 0005).
	Escalated bool `yaml:"escalated,omitempty"`
	// Effort overrides the profile's effort level, for a retry.
	Effort string `yaml:"effort,omitempty"`
	// Commits are the commits made by sessions that worked on the task. They
	// turn a later rejection into a revision with the right context.
	Commits []string `yaml:"commits,omitempty"`
	// Revises is the commit a revision reworks; its work lands as a fixup of
	// that commit (ADR 0003).
	Revises string `yaml:"revises,omitempty"`
	// Hunks are the rejections a revision carries, each as <hunk ID>@<seq>
	// of the review record it came from.
	Hunks []string `yaml:"hunks,omitempty"`
	// Usage is the task's share of each session that worked on it.
	Usage   []Usage   `yaml:"usage,omitempty"`
	Created time.Time `yaml:"created"`

	// State is the directory the task was read from.
	State State `yaml:"-"`
	// Body is the task's Markdown, after the frontmatter.
	Body string `yaml:"-"`
}

// Origin is where a task came from: the plan, a review decision, a question,
// an intake or the harness itself.
type Origin struct {
	Type string `yaml:"type"`
	Ref  string `yaml:"ref,omitempty"`
}

// Usage is the tokens a session spent, divided evenly among its tasks.
type Usage struct {
	Session       string  `yaml:"session"`
	InputTokens   int     `yaml:"inputTokens"`
	OutputTokens  int     `yaml:"outputTokens"`
	CacheCreation int     `yaml:"cacheCreation"`
	CacheRead     int     `yaml:"cacheRead"`
	CostUSD       float64 `yaml:"costUSD"`
}

// QuestionState is whether a question is waiting for the human.
type QuestionState string

// The question states.
const (
	QuestionOpen   QuestionState = "open"
	QuestionClosed QuestionState = "closed"
)

// Question is something an agent needs the human to decide. The task that
// raised it is blocked until it is answered (ADR 0009).
type Question struct {
	ID       string    `yaml:"id"`
	Task     string    `yaml:"task"`
	Created  time.Time `yaml:"created"`
	Answer   string    `yaml:"answer,omitempty"`
	Answered time.Time `yaml:"answered,omitempty"`

	State QuestionState `yaml:"-"`
	// Text is the question, the file's body.
	Text string `yaml:"-"`
}

// Store is the `.diatom/` directory of one repository.
type Store struct {
	// Root is the repository's `.diatom/` directory.
	Root string
}

// Open returns the store of the repository rooted at repo.
func Open(repo string) *Store {
	return &Store{Root: filepath.Join(repo, ".diatom")}
}

// Repo is the repository the store belongs to.
func (s *Store) Repo() string { return filepath.Dir(s.Root) }

// GoalDir is the directory of the named goal.
func (s *Store) GoalDir(goal string) string { return filepath.Join(s.Root, "goals", goal) }

// WorktreeDir is where a workstream's worktree lives.
func (s *Store) WorktreeDir(goal, ws string) string {
	return filepath.Join(s.GoalDir(goal), "worktrees", ws)
}

// SessionsDir holds the goal's agent sessions.
func (s *Store) SessionsDir(goal string) string {
	return filepath.Join(s.GoalDir(goal), "sessions")
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidName reports whether name can be a goal or workstream name: lowercase
// letters, digits and dashes, so it is safe as a directory and branch name.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a valid name: use lowercase letters, digits and dashes", name)
	}
	return nil
}

// CreateGoal writes a new goal. It fails if the goal already exists.
func (s *Store) CreateGoal(g *Goal) error {
	if err := ValidName(g.Name); err != nil {
		return err
	}
	for _, w := range g.Workstreams {
		if err := ValidName(w.Name); err != nil {
			return err
		}
	}
	dir := s.GoalDir(g.Name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("goal %s already exists", g.Name)
		}
		return err
	}
	return s.SaveGoal(g)
}

// SaveGoal rewrites a goal's goal.yaml atomically.
func (s *Store) SaveGoal(g *Goal) error {
	b, err := yaml.Marshal(g)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.GoalDir(g.Name), "goal.yaml"), b)
}

// Goal reads the named goal.
func (s *Store) Goal(name string) (*Goal, error) {
	b, err := os.ReadFile(filepath.Join(s.GoalDir(name), "goal.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no goal %s in %s", name, s.Repo())
	}
	if err != nil {
		return nil, err
	}
	g := &Goal{Name: name}
	if err := yaml.Unmarshal(b, g); err != nil {
		return nil, fmt.Errorf("goal %s: %w", name, err)
	}
	return g, nil
}

// Goals reads every goal in the store, oldest first.
func (s *Store) Goals() ([]*Goal, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "goals"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var goals []*Goal
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		g, err := s.Goal(e.Name())
		if err != nil {
			return nil, err
		}
		goals = append(goals, g)
	}
	slices.SortStableFunc(goals, func(a, b *Goal) int { return a.Created.Compare(b.Created) })
	return goals, nil
}

func (s *Store) taskPath(goal string, state State, id string) string {
	return filepath.Join(s.GoalDir(goal), "tasks", string(state), id+".md")
}

// AddTask assigns t the next id and writes it as pending.
func (s *Store) AddTask(goal string, t *Task) error {
	if t.Profile == "" {
		t.Profile = t.Kind.DefaultProfile()
	}
	dir := filepath.Join(s.GoalDir(goal), "tasks", string(Pending))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	t.State = Pending
	return createNext(
		filepath.Join(s.GoalDir(goal), "tasks"),
		dir,
		func(id string) ([]byte, error) {
			t.ID = id
			return marshal(t, t.Body)
		},
	)
}

// Task reads one task, wherever it is.
func (s *Store) Task(goal, id string) (*Task, error) {
	for _, st := range taskStates {
		t, err := readTask(s.taskPath(goal, st, id), st)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return t, err
	}
	return nil, fmt.Errorf("no task %s in goal %s", id, goal)
}

// Tasks reads every task of a goal, in id order.
func (s *Store) Tasks(goal string) ([]*Task, error) {
	var tasks []*Task
	for _, st := range taskStates {
		files, err := list(filepath.Join(s.GoalDir(goal), "tasks", string(st)))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			t, err := readTask(f, st)
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, t)
		}
	}
	slices.SortFunc(tasks, func(a, b *Task) int { return strings.Compare(a.ID, b.ID) })
	return tasks, nil
}

// SaveTask rewrites a task in place, atomically. The task keeps its state.
func (s *Store) SaveTask(goal string, t *Task) error {
	b, err := marshal(t, t.Body)
	if err != nil {
		return err
	}
	return writeAtomic(s.taskPath(goal, t.State, t.ID), b)
}

// Move saves t and then renames it into the state to. Each step is atomic, so
// a crash between them leaves the saved task in its old state.
func (s *Store) Move(goal string, t *Task, to State) error {
	if err := s.SaveTask(goal, t); err != nil {
		return err
	}
	if t.State == to {
		return nil
	}
	dst := s.taskPath(goal, to, t.ID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.taskPath(goal, t.State, t.ID), dst); err != nil {
		return err
	}
	t.State = to
	return nil
}

// AppendNote adds a note to the end of a task's body. It rereads the task so
// a note never overwrites one written since the caller last read it.
func (s *Store) AppendNote(goal, id, heading, text string) error {
	t, err := s.Task(goal, id)
	if err != nil {
		return err
	}
	t.Body = strings.TrimRight(
		t.Body,
		"\n",
	) + "\n\n## " + heading + "\n\n" + strings.TrimSpace(
		text,
	) + "\n"
	return s.SaveTask(goal, t)
}

func (s *Store) questionPath(goal string, state QuestionState, id string) string {
	return filepath.Join(s.GoalDir(goal), "questions", string(state), id+".md")
}

// AddQuestion assigns q the next id and writes it as open.
func (s *Store) AddQuestion(goal string, q *Question) error {
	dir := filepath.Join(s.GoalDir(goal), "questions", string(QuestionOpen))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	q.State = QuestionOpen
	return createNext(
		filepath.Join(s.GoalDir(goal), "questions"),
		dir,
		func(id string) ([]byte, error) {
			q.ID = id
			return marshal(q, q.Text)
		},
	)
}

// Questions reads a goal's questions in the given state, in id order.
func (s *Store) Questions(goal string, state QuestionState) ([]*Question, error) {
	files, err := list(filepath.Join(s.GoalDir(goal), "questions", string(state)))
	if err != nil {
		return nil, err
	}
	qs := make([]*Question, 0, len(files))
	for _, f := range files {
		q := &Question{State: state}
		body, err := readFront(f, q)
		if err != nil {
			return nil, err
		}
		q.Text = body
		qs = append(qs, q)
	}
	return qs, nil
}

// Answer records the human's answer on an open question. The question stays
// open until the harness applies the answer to its task and closes it.
func (s *Store) Answer(goal, id, answer string, now time.Time) error {
	qs, err := s.Questions(goal, QuestionOpen)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(qs, func(q *Question) bool { return q.ID == id })
	if i < 0 {
		return fmt.Errorf("no open question %s in goal %s", id, goal)
	}
	q := qs[i]
	q.Answer, q.Answered = strings.TrimSpace(answer), now
	b, err := marshal(q, q.Text)
	if err != nil {
		return err
	}
	return writeAtomic(s.questionPath(goal, QuestionOpen, id), b)
}

// CloseQuestion moves an answered question to closed.
func (s *Store) CloseQuestion(goal string, q *Question) error {
	dst := s.questionPath(goal, QuestionClosed, q.ID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.questionPath(goal, q.State, q.ID), dst); err != nil {
		return err
	}
	q.State = QuestionClosed
	return nil
}

// createNext writes a new file in dir under the next free four-digit id,
// counting every file under root so ids stay unique across states. render
// returns the file's content for a candidate id. O_EXCL makes two writers
// racing for the same id take turns.
func createNext(root, dir string, render func(id string) ([]byte, error)) error {
	next, err := maxID(root)
	if err != nil {
		return err
	}
	for {
		next++
		candidate := fmt.Sprintf("%04d", next)
		b, err := render(candidate)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(
			filepath.Join(dir, candidate+".md"),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o644,
		)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, werr := f.Write(b)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}
}

// maxID is the largest numeric file name under root's state directories.
func maxID(root string) (int, error) {
	states, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	top := 0
	for _, st := range states {
		files, err := list(filepath.Join(root, st.Name()))
		if err != nil {
			return 0, err
		}
		for _, f := range files {
			if n, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".md")); err == nil {
				top = max(top, n)
			}
		}
	}
	return top, nil
}

// list returns the Markdown files in dir, skipping in-progress temp files.
func list(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return files, nil
}

func readTask(path string, st State) (*Task, error) {
	t := &Task{State: st}
	body, err := readFront(path, t)
	if err != nil {
		return nil, err
	}
	t.Body = body
	return t, nil
}

// readFront decodes the frontmatter of the file at path into v and returns
// the body after it.
func readFront(path string, v any) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	front, body, ok := bytes.Cut(bytes.TrimPrefix(b, []byte("---\n")), []byte("\n---\n"))
	if !bytes.HasPrefix(b, []byte("---\n")) || !ok {
		return "", fmt.Errorf("%s: no YAML frontmatter", path)
	}
	if err := yaml.Unmarshal(front, v); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return strings.TrimLeft(string(body), "\n"), nil
}

// marshal renders v as frontmatter followed by body.
func marshal(v any, body string) ([]byte, error) {
	front, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n")
	if body != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(body, "\n"))
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}

// writeAtomic replaces path with b through a temp file in the same directory.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name())
		return werr
	}
	return os.Rename(f.Name(), path)
}
