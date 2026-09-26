// Package panes holds the workspace's panes besides the reviewer (ADR 0007):
// status, questions and intake. Each is a separate Bubble Tea program that
// reads and writes .diatom/ state, so any of them can restart without the
// others noticing.
package panes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/registry"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/tui"
)

// refreshEvery is how often a pane rereads the state.
const refreshEvery = 3 * time.Second

// Env is what every pane reads and writes.
type Env struct {
	Registry registry.Registry
	Focus    focus.File
	Paths    config.Paths
	Now      func() time.Time
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// goalRow is one goal in the status pane.
type goalRow struct {
	repo       string
	goal       *queue.Goal
	counts     map[queue.State]int
	questions  int
	toReview   int
	activeWork []string
}

// Status shows every goal in every known repo and the sessions running now,
// and sets the focus the other panes follow.
type Status struct {
	ctx   context.Context
	env   Env
	rows  []goalRow
	sel   int
	focus focus.Focus

	// hunks caches each commit's hunk count; commits never change.
	hunks map[string]int

	picking bool
	picker  textinput.Model
	repos   []string
	pickSel int

	width, height int
	flash         string
	err           error
}

// NewStatus loads the status pane.
func NewStatus(ctx context.Context, env Env) *Status {
	in := textinput.New()
	in.Placeholder = "filter repos"
	s := &Status{ctx: ctx, env: env, hunks: map[string]int{}, picker: in, width: 80, height: 24}
	s.reload()
	return s
}

func (s *Status) reload() {
	s.err = nil
	fc, err := s.env.Focus.Read()
	if err != nil {
		s.err = err
	}
	s.focus = fc
	repos, err := s.env.Registry.List()
	if err != nil {
		s.err = err
		return
	}
	var rows []goalRow
	for _, repo := range repos {
		store := queue.Open(repo)
		goals, err := store.Goals()
		if err != nil {
			s.err = err
			continue
		}
		for _, g := range goals {
			if g.State == queue.GoalDone {
				continue
			}
			row, err := s.row(store, g)
			if err != nil {
				s.err = err
				continue
			}
			rows = append(rows, row)
		}
	}
	keep := s.selected()
	s.rows = rows
	if keep != nil {
		s.sel = max(slices.IndexFunc(rows, func(r goalRow) bool {
			return r.repo == keep.repo && r.goal.Name == keep.goal.Name
		}), 0)
	}
	s.sel = min(s.sel, max(len(rows)-1, 0))
}

func (s *Status) row(store *queue.Store, g *queue.Goal) (goalRow, error) {
	row := goalRow{repo: store.Repo(), goal: g, counts: map[queue.State]int{}}
	tasks, err := store.Tasks(g.Name)
	if err != nil {
		return row, err
	}
	var commits []string
	for _, t := range tasks {
		row.counts[t.State]++
		if t.State == queue.Active {
			row.activeWork = append(row.activeWork, t.Workstream)
		}
		for _, sha := range t.Commits {
			if !slices.Contains(commits, sha) {
				commits = append(commits, sha)
			}
		}
	}
	qs, err := store.Questions(g.Name, queue.QuestionOpen)
	if err != nil {
		return row, err
	}
	for _, q := range qs {
		if q.Answer == "" {
			row.questions++
		}
	}
	rev := review.Store{Dir: store.GoalDir(g.Name)}
	repo := git.Repo{Dir: store.Repo()}
	for _, sha := range commits {
		n, ok := s.hunks[sha]
		if !ok {
			hunks, err := review.Hunks(s.ctx, repo, sha)
			if err != nil {
				return row, err
			}
			n = len(hunks)
			s.hunks[sha] = n
		}
		rec, err := rev.Load(sha)
		if err != nil {
			return row, err
		}
		decided := 0
		for _, r := range rec.Hunks {
			if r.Decision != review.Defer {
				decided++
			}
		}
		row.toReview += max(n-decided, 0)
	}
	slices.Sort(row.activeWork)
	row.activeWork = slices.Compact(row.activeWork)
	return row, nil
}

func (s *Status) selected() *goalRow {
	if s.sel < 0 || s.sel >= len(s.rows) {
		return nil
	}
	return &s.rows[s.sel]
}

// Init implements tea.Model.
func (s *Status) Init() tea.Cmd { return tick() }

// Update implements tea.Model.
func (s *Status) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tickMsg:
		if !s.picking {
			s.reload()
		}
		return s, tick()
	case tea.KeyPressMsg:
		if s.picking {
			return s.updatePicker(msg)
		}
		return s.updateKey(msg)
	}
	return s, nil
}

func (s *Status) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s.flash = ""
	row := s.selected()
	switch msg.String() {
	case "q", "ctrl+c":
		return s, tea.Quit
	case "j", "down":
		s.sel = min(s.sel+1, max(len(s.rows)-1, 0))
	case "k", "up":
		s.sel = max(s.sel-1, 0)
	case "enter", "f":
		if row != nil {
			s.setFocus(focus.Focus{Repo: row.repo, Goal: row.goal.Name})
		}
	case "p":
		if row != nil {
			s.toggleParked(row)
		}
	case "P":
		if row != nil {
			row.goal.Pinned = !row.goal.Pinned
			what := "unpinned"
			if row.goal.Pinned {
				what = "pinned"
			}
			s.save(row, what)
		}
	case "o":
		s.openPicker()
		return s, s.picker.Focus()
	case "r":
		s.reload()
	}
	return s, nil
}

// toggleParked parks an active goal or resumes a parked one. Parking stops
// new tasks and keeps everything else, so resuming loses nothing (ADR 0003).
func (s *Status) toggleParked(row *goalRow) {
	switch row.goal.State {
	case queue.GoalActive:
		row.goal.State = queue.GoalParked
		s.save(row, "parked")
	case queue.GoalParked:
		row.goal.State = queue.GoalActive
		s.save(row, "resumed")
	default:
		s.flash = fmt.Sprintf(
			"%s is %s; only an active or parked goal can be parked or resumed",
			row.goal.Name,
			row.goal.State,
		)
	}
}

func (s *Status) save(row *goalRow, what string) {
	if err := queue.Open(row.repo).SaveGoal(row.goal); err != nil {
		s.err = err
		return
	}
	s.flash = fmt.Sprintf("%s %s", row.goal.Name, what)
}

func (s *Status) setFocus(fc focus.Focus) {
	if err := s.env.Focus.Write(fc); err != nil {
		s.err = err
		return
	}
	s.focus = fc
	s.flash = "focused " + describe(fc)
}

func describe(fc focus.Focus) string {
	if fc.Repo == "" {
		return "nothing"
	}
	if fc.Goal == "" {
		return filepath.Base(fc.Repo)
	}
	return filepath.Base(fc.Repo) + "/" + fc.Goal
}

// View implements tea.Model.
func (s *Status) View() tea.View {
	v := tea.NewView(s.render())
	v.AltScreen = true
	return v
}

func (s *Status) render() string {
	if s.picking {
		return s.renderPicker()
	}
	var b strings.Builder
	b.WriteString(tui.Bold("status") + tui.Dim(" · focus "+describe(s.focus)) + "\n\n")
	if len(s.rows) == 0 {
		b.WriteString(
			"No goals yet. Press o to pick a repo, then describe the goal in the intake pane.\n",
		)
	}
	for i, r := range s.rows {
		mark := "  "
		if i == s.sel {
			mark = tui.Color("› ", tui.Cyan)
		}
		focused := " "
		if r.repo == s.focus.Repo && r.goal.Name == s.focus.Goal {
			focused = tui.Color("●", tui.Cyan)
		}
		name := filepath.Base(r.repo) + "/" + r.goal.Name
		if r.goal.Pinned {
			name += " 📌"
		}
		fmt.Fprintf(&b, "%s%s %s %s\n", mark, focused, tui.Bold(name), stateColor(r.goal.State))
		fmt.Fprintf(
			&b,
			"      %s",
			tui.Dim(fmt.Sprintf(
				"tasks %d pending · %d active · %d blocked · %d done",
				r.counts[queue.Pending],
				r.counts[queue.Active],
				r.counts[queue.Blocked],
				r.counts[queue.Done],
			)),
		)
		if r.questions > 0 {
			b.WriteString(" · " + tui.Color(fmt.Sprintf("%d questions", r.questions), tui.Magenta))
		}
		if r.toReview > 0 {
			b.WriteString(" · " + tui.Color(fmt.Sprintf("%d to review", r.toReview), tui.Yellow))
		}
		b.WriteString("\n")
		for _, ws := range r.activeWork {
			fmt.Fprintf(
				&b,
				"      %s %s\n",
				tui.Color("▶ "+ws, tui.Green),
				tui.Dim(lastEvent(r.repo, r.goal.Name, ws)),
			)
		}
	}
	if s.err != nil {
		b.WriteString("\n" + tui.Color(s.err.Error(), tui.Red) + "\n")
	}
	if s.flash != "" {
		b.WriteString("\n" + tui.Color(s.flash, tui.Cyan) + "\n")
	}
	b.WriteString(
		"\n" + tui.Dim("enter focus · p park/resume · P pin · o open repo · r refresh · q quit"),
	)
	return b.String()
}

func stateColor(st queue.GoalState) string {
	c := map[queue.GoalState]int{
		queue.GoalActive: tui.Green, queue.GoalParked: tui.Yellow, queue.GoalPlanning: tui.Blue,
	}[st]
	return tui.Color(string(st), c)
}

// lastEvent is the latest progress line of a workstream's running session.
func lastEvent(repo, goal, ws string) string {
	dirs, err := filepath.Glob(filepath.Join(queue.Open(repo).SessionsDir(goal), "*-"+ws))
	if err != nil || len(dirs) == 0 {
		return ""
	}
	slices.Sort(dirs)
	f, err := os.Open(filepath.Join(dirs[len(dirs)-1], "events.jsonl"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		last = sc.Text()
	}
	var ev struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(last), &ev) != nil {
		return ""
	}
	text, _, _ := strings.Cut(strings.TrimSpace(ev.Text), "\n")
	if len(text) > 90 {
		text = text[:90] + "…"
	}
	return text
}

// openPicker lists the git repos under the search roots.
func (s *Status) openPicker() {
	home, err := config.LoadHome(s.env.Paths)
	if err != nil {
		s.err = err
		return
	}
	s.repos = nil
	for _, root := range home.SearchRoots {
		s.repos = append(s.repos, findRepos(s.env.Paths.Expand(root), 4)...)
	}
	slices.Sort(s.repos)
	s.picking, s.pickSel = true, 0
	s.picker.Reset()
}

// findRepos returns the git repos under root, at most depth levels down.
func findRepos(root string, depth int) []string {
	var repos []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil // an unreadable directory is skipped, not fatal
		}
		rel, _ := filepath.Rel(root, path)
		if rel != "." && strings.Count(rel, string(filepath.Separator)) >= depth {
			return filepath.SkipDir
		}
		switch d.Name() {
		case "node_modules", "vendor", ".diatom":
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			repos = append(repos, path)
			return filepath.SkipDir
		}
		return nil
	})
	return repos
}

func (s *Status) matches() []string {
	filter := strings.ToLower(strings.TrimSpace(s.picker.Value()))
	var out []string
	for _, r := range s.repos {
		if strings.Contains(strings.ToLower(r), filter) {
			out = append(out, r)
		}
	}
	return out
}

func (s *Status) updatePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	matches := s.matches()
	switch msg.String() {
	case "esc":
		s.picking = false
		return s, nil
	case "down", "ctrl+n":
		s.pickSel = min(s.pickSel+1, max(len(matches)-1, 0))
		return s, nil
	case "up", "ctrl+p":
		s.pickSel = max(s.pickSel-1, 0)
		return s, nil
	case "enter":
		if s.pickSel < len(matches) {
			repo := matches[s.pickSel]
			if err := s.env.Registry.Add(repo); err != nil {
				s.err = err
			}
			s.picking = false
			s.setFocus(focus.Focus{Repo: repo})
			s.flash += "; describe a new goal in the intake pane"
			s.reload()
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.picker, cmd = s.picker.Update(msg)
	s.pickSel = 0
	return s, cmd
}

func (s *Status) renderPicker() string {
	var b strings.Builder
	b.WriteString(tui.Bold("open a repo") + "\n\n" + s.picker.View() + "\n\n")
	matches := s.matches()
	rows := max(s.height-8, 3)
	start := max(0, s.pickSel-rows+1)
	for i := start; i < len(matches) && i < start+rows; i++ {
		mark := "  "
		if i == s.pickSel {
			mark = tui.Color("› ", tui.Cyan)
		}
		b.WriteString(mark + matches[i] + "\n")
	}
	if len(matches) == 0 {
		b.WriteString(tui.Dim("no repo under the search roots matches") + "\n")
	}
	b.WriteString("\n" + tui.Dim("enter open · esc cancel"))
	return b.String()
}
