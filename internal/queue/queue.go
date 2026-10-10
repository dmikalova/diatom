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
//	  notes/<state>/<id>.md      open or read
//	  sessions/<id>/             one agent session's spec, report and log
//	  worktrees/<workstream>/    one git worktree per workstream
//
// The repo also keeps `.diatom/landing/`, the one worktree every goal is
// laid out and gated in.
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
	// GoalDropped is a goal given up on: none of its work lands, and its
	// files stay only so what it cost is still counted.
	GoalDropped GoalState = "dropped"
)

// IntakeGoal holds the repo's triage tasks and their questions: triage
// sorts intake for the whole repo, not for one goal (ADR 0009). It is never
// listed with the goals, and no goal can take its name, which isn't a valid
// one.
const IntakeGoal = "_intake"

// Goal is a unit of intent submitted by the human.
type Goal struct {
	// Name is the goal's directory name and branch prefix. It is not stored
	// in goal.yaml.
	Name  string `yaml:"-"`
	Title string `yaml:"title"`
	// Description says in a line what the goal is for, so agents working on
	// other goals and the human answering its questions know it at a glance.
	// Triage writes it.
	Description string    `yaml:"description,omitempty"`
	State       GoalState `yaml:"state"`
	// Base is the branch the integration branch started from.
	Base string `yaml:"base"`
	// Branch is the branch the goal's pull request lives on, for a tracker
	// that links the ticket to a branch it named itself, such as Linear.
	// Empty means diatom names it (ADR 0014).
	Branch string `yaml:"branch,omitempty"`
	// Ticket is the goal's ticket in the repo's tracker, such as DIP-4117.
	// It names the scope of the pull request's title, so the repos that
	// lint titles take it (ADR 0014).
	Ticket  string    `yaml:"ticket,omitempty"`
	Created time.Time `yaml:"created"`
	// Finished is when the goal was found landed upstream, or was dropped.
	Finished time.Time `yaml:"finished,omitempty"`
	// Reason is why a dropped goal was given up on.
	Reason      string       `yaml:"reason,omitempty"`
	Workstreams []Workstream `yaml:"workstreams,omitempty"`
	// After names the goals this one waits for: none of its work starts,
	// grilling included, until each is finished, merged upstream with its
	// checks passing (ADR 0003). Each comes off once it has finished and
	// what it landed is merged into the goal's branch.
	After []string `yaml:"after,omitempty"`
	// CaughtUp names goals of After an older diatom merged in while keeping
	// them listed; the scheduler takes them off After and clears it.
	CaughtUp []string `yaml:"caughtUp,omitempty"`
}

// Workstream is a named line of work within a goal, with its own branch and
// worktree.
type Workstream struct {
	Name      string   `yaml:"name"`
	DependsOn []string `yaml:"dependsOn,omitempty"`
}

// ticketRe matches a tracker's ticket id, which Linear, Jira and Shortcut
// all write the same way: a team key, a dash, and a number.
var ticketRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)

// ParseTicket is a ticket id as a goal stores it, upper-cased, and whether
// what it was given is one at all.
func ParseTicket(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return strings.ToUpper(s), ticketRe.MatchString(s)
}

// IntegrationBranch is the branch that collects all of a goal's work.
func (g *Goal) IntegrationBranch() string { return "diatom/" + g.Name + "/integration" }

// Over reports whether the goal's life is done, whether it landed or was
// dropped: nothing more happens on it, and nothing waits for it.
func (g *Goal) Over() bool { return g.State == GoalFinished || g.State == GoalDropped }

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
	// MCPServers are the connectors the task needs, by catalog name: the
	// plan's, plus any a session asked for while working on it (ADR 0013).
	MCPServers []string `yaml:"mcpServers,omitempty"`
	// CI is the commit whose checks the task waits for, and CIRounds how many
	// verdicts it has already had. A task waiting on a commit is not
	// scheduled, which parks it until diatom has the verdict (ADR 0014).
	CI       string `yaml:"ci,omitempty"`
	CIRounds int    `yaml:"ciRounds,omitempty"`
	// CILabels are the labels diatom put on the pull request for this round,
	// and takes off again with the verdict. Only the ones it added itself.
	CILabels []string `yaml:"ciLabels,omitempty"`
	// Commits are the commits made by sessions that worked on the task. They
	// turn a later rejection into a revision with the right context.
	Commits []string `yaml:"commits,omitempty"`
	// Revises is the commit a revision reworks; its work lands as a fixup of
	// that commit (ADR 0003).
	Revises string `yaml:"revises,omitempty"`
	// Merge is the branch a conflict task merges into its workstream after
	// the integration branch: the base branch, when it has moved on and
	// conflicts with the goal.
	Merge string `yaml:"merge,omitempty"`
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

// SettledLanding reports whether t holds how an agent settled the conflicts
// of a landing's rebase, as an older diatom put up for review. Landing is
// mechanical now, and such a layout is made again, so the task's commits are
// reviewed no more.
func (t *Task) SettledLanding() bool { return t.Origin.Type == "landing" }

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

// Question is something an agent needs the human to decide, or to do. The
// task that raised it is blocked until it is answered (ADR 0009).
type Question struct {
	ID   string `yaml:"id"`
	Task string `yaml:"task"`
	// Manual marks steps the human has to do by hand, such as running tofu
	// apply, rather than a decision: its answer says they are done, or what
	// happened instead.
	Manual   bool      `yaml:"manual,omitempty"`
	Created  time.Time `yaml:"created"`
	Answer   string    `yaml:"answer,omitempty"`
	Answered time.Time `yaml:"answered,omitempty"`

	State QuestionState `yaml:"-"`
	// Text is the question, the file's body.
	Text string `yaml:"-"`
}

// NoteState is whether a note still waits for the human to read it.
type NoteState string

// The note states.
const (
	NoteOpen NoteState = "open"
	NoteRead NoteState = "read"
)

// Note is something an agent wants the human to know and needs no answer
// for. It waits in Next until they have read it, and never blocks its task
// (ADR 0009).
type Note struct {
	ID      string    `yaml:"id"`
	Task    string    `yaml:"task"`
	Created time.Time `yaml:"created"`

	State NoteState `yaml:"-"`
	// Text is the note, the file's body.
	Text string `yaml:"-"`
}

// ProblemState is whether a problem still holds its goal up.
type ProblemState string

// The problem states.
const (
	ProblemOpen ProblemState = "open"
	ProblemDone ProblemState = "done"
)

// ProblemKind says what failed, which decides what the human may do about it
// beyond retrying, giving up and replying (ADR 0014).
type ProblemKind string

// The problem kinds. Each one a goal's work can hit in the background, and
// each repo-level one, which lives on the intake goal.
const (
	// ProblemCommit is the harness failing to commit or stash a session's
	// work, ProblemLand failing to merge or push a goal, and ProblemPR
	// failing to open or update its pull requests.
	ProblemCommit ProblemKind = "commit"
	ProblemLand   ProblemKind = "land"
	ProblemPR     ProblemKind = "pr"
	// ProblemPRClosed is a pull request of a done goal closed without being
	// merged: the goal can go no further until the human says how.
	ProblemPRClosed ProblemKind = "pr-closed"
	// ProblemWorktree is a worktree whose repo is gone, ProblemRepo a repo
	// diatom can't key because it has no origin, and ProblemCheckout a
	// second checkout of a repo diatom already works in (ADR 0014).
	ProblemWorktree  ProblemKind = "worktree"
	ProblemRepo      ProblemKind = "repo"
	ProblemCheckout  ProblemKind = "checkout"
	ProblemConfigKey ProblemKind = "config-key"
)

// Problem is something diatom itself hit in the background, where no human
// was watching to see it fail. It holds its goal up until the human deals
// with it, as a question holds up its task (ADR 0014).
type Problem struct {
	ID   string      `yaml:"id"`
	Kind ProblemKind `yaml:"kind"`
	// What is the one line the human reads first: what diatom was doing and
	// how it went wrong.
	What string `yaml:"what"`
	// Op is what diatom was doing, which with the goal keys the problem: the
	// same failure hit again replaces this one and bumps Count, rather than
	// filling the queue with copies.
	Op      string    `yaml:"op"`
	Count   int       `yaml:"count"`
	Created time.Time `yaml:"created"`
	Last    time.Time `yaml:"last"`
	// Reply is what the human told the agent to do about it, and Replied
	// when. The harness turns a reply into a task of the goal's.
	Reply   string    `yaml:"reply,omitempty"`
	Replied time.Time `yaml:"replied,omitempty"`
	// Fix is a value a kind's own action took, such as the URL of the pull
	// request that replaced a closed one.
	Fix string `yaml:"fix,omitempty"`

	State ProblemState `yaml:"-"`
	// Text is the whole output of what failed, the file's body. It can be
	// long: Next shows its tail and the editor opens the rest.
	Text string `yaml:"-"`
}

// Store is one repository's queue: its goals, their tasks, and everything
// diatom keeps about them. The directory it is in is not in the repository
// (ADR 0013): Root says where it is and repo which repository it is of.
type Store struct {
	// Root is the directory the store is in.
	Root string
	// repo is the repository Root is the queue of, empty for the old layout
	// where Root was the repository's own `.diatom/` directory.
	repo string
	// key names the repository by its origin, which is what the config's
	// per-repo blocks are keyed by (ADR 0013).
	key string
}

// Open returns the store in the repository's own `.diatom/` directory: the
// layout before ADR 0013, which the migration reads and tests still use.
func Open(repo string) *Store {
	return &Store{Root: filepath.Join(repo, ".diatom")}
}

// At returns the store of the repository rooted at repo, kept in root
// outside it. key names the repository by its origin.
func At(repo, root, key string) *Store {
	return &Store{Root: root, repo: repo, key: key}
}

// Key names the repository by its origin, "" when it is not known.
func (s *Store) Key() string { return s.key }

// Repo is the repository the store belongs to.
func (s *Store) Repo() string {
	if s.repo != "" {
		return s.repo
	}
	return filepath.Dir(s.Root)
}

// GoalDir is the directory of the named goal.
func (s *Store) GoalDir(goal string) string { return filepath.Join(s.Root, "goals", goal) }

// WorktreeDir is where a workstream's worktree lives.
func (s *Store) WorktreeDir(goal, ws string) string {
	return filepath.Join(s.GoalDir(goal), "worktrees", ws)
}

// LandingDir is the worktree every goal is laid out and gated in. It is one
// per repo and it is kept between landings, so what the gate builds there,
// such as installed dependencies, is still there the next time.
func (s *Store) LandingDir() string { return filepath.Join(s.Root, "landing") }

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
	if err := ValidName(g.Name); err != nil && g.Name != IntakeGoal {
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
		if !e.IsDir() || e.Name() == IntakeGoal {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.GoalDir(e.Name()), "goal.yaml")); errors.Is(
			err,
			fs.ErrNotExist,
		) {
			// A goal being created: CreateGoal makes its directory first.
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
func (s *Store) AddTask(goal string, t *Task) error { return s.addTask(goal, t, Pending) }

// AddDone records a task as done from the start: work diatom did itself,
// such as a landing's, whose commits are for review.
func (s *Store) AddDone(goal string, t *Task) error { return s.addTask(goal, t, Done) }

func (s *Store) addTask(goal string, t *Task, state State) error {
	if t.Profile == "" {
		t.Profile = t.Kind.DefaultProfile()
	}
	dir := filepath.Join(s.GoalDir(goal), "tasks", string(state))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	t.State = state
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

func (s *Store) notePath(goal string, state NoteState, id string) string {
	return filepath.Join(s.GoalDir(goal), "notes", string(state), id+".md")
}

// AddNote assigns n the next id and writes it unread.
func (s *Store) AddNote(goal string, n *Note) error {
	dir := filepath.Join(s.GoalDir(goal), "notes", string(NoteOpen))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	n.State = NoteOpen
	return createNext(
		filepath.Join(s.GoalDir(goal), "notes"),
		dir,
		func(id string) ([]byte, error) {
			n.ID = id
			return marshal(n, n.Text)
		},
	)
}

// Notes reads a goal's notes in the given state, in id order.
func (s *Store) Notes(goal string, state NoteState) ([]*Note, error) {
	files, err := list(filepath.Join(s.GoalDir(goal), "notes", string(state)))
	if err != nil {
		return nil, err
	}
	notes := make([]*Note, 0, len(files))
	for _, f := range files {
		n := &Note{State: state}
		body, err := readFront(f, n)
		if err != nil {
			return nil, err
		}
		n.Text = body
		notes = append(notes, n)
	}
	return notes, nil
}

// ReadNote moves a note the human has read out of their way.
func (s *Store) ReadNote(goal string, n *Note) error {
	dst := s.notePath(goal, NoteRead, n.ID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.notePath(goal, n.State, n.ID), dst); err != nil {
		return err
	}
	n.State = NoteRead
	return nil
}

func (s *Store) problemPath(goal string, state ProblemState, id string) string {
	return filepath.Join(s.GoalDir(goal), "problems", string(state), id+".md")
}

// ProblemFile is where a problem is written, for an editor to open.
func (s *Store) ProblemFile(goal string, state ProblemState, id string) string {
	return s.problemPath(goal, state, id)
}

// Problems reads a goal's problems in the given state, in id order.
func (s *Store) Problems(goal string, state ProblemState) ([]*Problem, error) {
	files, err := list(filepath.Join(s.GoalDir(goal), "problems", string(state)))
	if err != nil {
		return nil, err
	}
	ps := make([]*Problem, 0, len(files))
	for _, f := range files {
		p := &Problem{State: state}
		body, err := readFront(f, p)
		if err != nil {
			return nil, err
		}
		p.Text = body
		ps = append(ps, p)
	}
	return ps, nil
}

// HitProblem records that diatom hit p on the goal. The same failure, by
// kind and op, replaces the open one and bumps its count: a loop the human
// hasn't looked at yet is one problem, not a hundred.
func (s *Store) HitProblem(goal string, p *Problem) error {
	open, err := s.Problems(goal, ProblemOpen)
	if err != nil {
		return err
	}
	p.State, p.Last = ProblemOpen, p.Created
	if i := slices.IndexFunc(open, func(o *Problem) bool {
		return o.Kind == p.Kind && o.Op == p.Op
	}); i >= 0 {
		p.ID, p.Count, p.Created = open[i].ID, open[i].Count+1, open[i].Created
		// A reply the human already gave was for the try that just failed
		// again, so it is spent.
		b, err := marshal(p, p.Text)
		if err != nil {
			return err
		}
		return writeAtomic(s.problemPath(goal, ProblemOpen, p.ID), b)
	}
	dir := filepath.Join(s.GoalDir(goal), "problems", string(ProblemOpen))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.Count = 1
	return createNext(
		filepath.Join(s.GoalDir(goal), "problems"),
		dir,
		func(id string) ([]byte, error) {
			p.ID = id
			return marshal(p, p.Text)
		},
	)
}

// SaveProblem writes an open problem back, as when the human replies to it
// or hands it the value its kind asked for.
func (s *Store) SaveProblem(goal string, p *Problem) error {
	b, err := marshal(p, p.Text)
	if err != nil {
		return err
	}
	return writeAtomic(s.problemPath(goal, ProblemOpen, p.ID), b)
}

// CloseProblem moves a problem the human has dealt with out of the way, so
// the goal runs again.
func (s *Store) CloseProblem(goal string, p *Problem) error {
	dst := s.problemPath(goal, ProblemDone, p.ID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.problemPath(goal, p.State, p.ID), dst); err != nil {
		return err
	}
	p.State = ProblemDone
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
