// Package finish lays a done goal's work out for landing (ADR 0003). The
// goal's commits are replayed onto its base branch without the merges, each
// fixup squashed into the commit it revises, and grouped by workstream into
// a stack of pull requests, one per workstream in dependency order.
package finish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dmikalova/diatom/internal/commitmsg"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// worktreeName is the worktree the commits are replayed in, removed again
// once they are. It can't be a workstream's name.
const worktreeName = "_finish"

// fileName holds the last layout, in the goal's directory.
const fileName = "finish.yaml"

// carryMessage is the message of the commit that carries what the goal's
// merges changed, which replaying the commits alone leaves out.
const carryMessage = `fix: keep what resolving the goal's merges changed

The goal's workstreams were merged into each other as the work went on, and
conflict and gate-repair tasks fixed those merges. Replayed one after
another, the commits leave those fixes out, so this commit carries them.
`

// squashMessage opens the message of the commit that a commit git can't
// replay on the base, and every commit after it, are squashed into.
const squashMessage = `%s

%s changed the same code as the goal while it ran, so git couldn't replay
these commits onto its tip one at a time. They are squashed into one commit
holding the files of the reviewed merge of %s into the goal.
`

// PR is one pull request of the stack: one workstream's commits, on top of
// the pull request before it.
type PR struct {
	// Workstream is empty when the whole goal is one pull request.
	Workstream string `yaml:"workstream,omitempty"`
	Branch     string `yaml:"branch"`
	Tip        string `yaml:"tip"`
	// Commits are the subjects of the pull request's commits, in order.
	Commits []string `yaml:"commits"`
	// Gate is the gate run on the pull request's tip; nil when no gate ran.
	Gate *Gate `yaml:"gate,omitempty"`
}

// Gate is how the gate went on a pull request's tip.
type Gate struct {
	Passed bool   `yaml:"passed"`
	Output string `yaml:"output,omitempty"`
}

// Result is a goal laid out for landing.
type Result struct {
	// Base and Integration are the commits it was laid out from: the base
	// branch's tip and the integration branch's.
	Base        string `yaml:"base"`
	Integration string `yaml:"integration"`
	// Final is the branch holding every commit, the last pull request's tip.
	Final string `yaml:"final"`
	// Stack is the pull requests in the order they land.
	Stack []PR `yaml:"stack"`
	// Unstacked says why the workstreams couldn't be put one after another,
	// leaving the goal as one pull request in the order its commits were made.
	Unstacked string `yaml:"unstacked,omitempty"`
	// Squashed are the subjects of the commits squashed into the last one,
	// because the base moved on in the goal's own code and git couldn't
	// replay them onto it one at a time.
	Squashed []string `yaml:"squashed,omitempty"`
	// Settled is how an older diatom had an agent settle the conflicts of a
	// rebase, unreviewed now that landing is mechanical: such a layout is
	// made again.
	Settled []yaml.Node `yaml:"settlements,omitempty"`
	// Carried is set when the last commit carries what the goal's merges
	// changed.
	Carried bool      `yaml:"carried,omitempty"`
	Built   time.Time `yaml:"built"`
	// Landing is how far the goal is on its way upstream.
	Landing *Landing `yaml:"landing,omitempty"`
}

// Tip is the commit holding all of the goal's work.
func (r *Result) Tip() string { return r.Stack[len(r.Stack)-1].Tip }

// Options set how a goal is laid out.
type Options struct {
	// Gate is the command run on each pull request's tip; empty skips it.
	Gate string
	// RunGate runs the gate; nil uses gate.Run.
	RunGate gate.Runner
	// Timeout is how long the gate may run before it counts as stuck; 0
	// never gives up.
	Timeout time.Duration
	Now     func() time.Time
	// Progress, when set, is told each step as it happens, and the gate's
	// output as it comes.
	Progress io.Writer
}

// step tells opts' progress what is happening.
func (o Options) step(format string, args ...any) {
	if o.Progress != nil {
		_, _ = fmt.Fprintf(o.Progress, format+"\n", args...)
	}
}

// FinalBranch is the branch a goal is laid out on.
func FinalBranch(g *queue.Goal) string { return "diatom/" + g.Name + "/final" }

// ReviewBranch is the branch the goal's pull request lives on, from its first
// commit to its merge. GitHub cannot move a pull request's head branch, so
// the first of the stack has to sit on a branch chosen before there is a
// stack, for the pull request opened early to survive landing (ADR 0014).
func ReviewBranch(g *queue.Goal) string { return "diatom/" + g.Name + "/review" }

// prBranch is the branch of one pull request of a goal's stack.
func prBranch(g *queue.Goal, ws string) string { return "diatom/" + g.Name + "/pr/" + ws }

// ConflictError is a commit that doesn't apply where it was replayed.
type ConflictError struct {
	Commit, Subject string
	Files           []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf(
		"replaying %s %q conflicts in %s",
		short(e.Commit),
		e.Subject,
		strings.Join(e.Files, ", "),
	)
}

// Build lays a goal out onto its base branch's tip and saves the result. It
// first tries one pull request per workstream; when a workstream's commits
// don't apply on top of the ones before, the goal becomes one pull request in
// the order its commits were made.
func Build(ctx context.Context, s *queue.Store, g *queue.Goal, opts Options) (*Result, error) {
	repo := git.Repo{Dir: s.Repo()}
	base, err := repo.RevParse(ctx, BaseTip(ctx, repo, g))
	if err != nil {
		return nil, err
	}
	integration, err := repo.RevParse(ctx, g.IntegrationBranch())
	if err != nil {
		return nil, err
	}
	want, err := expectedTree(ctx, repo, g, base, integration)
	if err != nil {
		return nil, err
	}
	commits, err := goalCommits(ctx, repo, base, integration)
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, fmt.Errorf("goal %s has no commits that %s lacks", g.Name, g.Base)
	}
	owner, err := owners(ctx, s, g, repo, base)
	if err != nil {
		return nil, err
	}

	wt := git.Repo{Dir: s.WorktreeDir(g.Name, worktreeName)}
	if err := repo.EnsureDetached(ctx, wt.Dir, base); err != nil {
		return nil, err
	}
	defer func() {
		// Nothing is kept in it; the branches hold the result.
		_, _ = repo.Run(context.WithoutCancel(ctx), "worktree", "remove", "--force", wt.Dir)
	}()

	res := &Result{Base: base, Integration: integration, Final: FinalBranch(g)}
	opts.step("Putting the goal's %d commits onto %s's tip", len(commits), g.Base)
	stack, err := replayOnto(ctx, wt, base, commits, owner, g, res)
	if conflict := (*ConflictError)(nil); errors.As(err, &conflict) {
		// The base moved on in the goal's own code, and the catch-up's
		// reviewed resolution doesn't settle every commit git replays. The
		// files the goal lands with are known, the reviewed merge's, so
		// the commits from the first git can't replay on are squashed into
		// one commit of them, rather than settled again.
		opts.step("%s changed the same code (%s): squashing that commit and the ones after it "+
			"into the reviewed merge", g.Base, conflict)
		res.Unstacked = conflict.Error()
		var pr PR
		pr, res.Squashed, err = replaySquashing(ctx, wt, base,
			arrange(commits, nil, []string{""})[0].steps, want, g.Base)
		stack = nil
		if err == nil && pr.Tip != base {
			stack = []PR{pr}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(stack) == 0 {
		return nil, fmt.Errorf("every commit of goal %s is already on %s", g.Name, g.Base)
	}
	last := &stack[len(stack)-1]
	if tree, err := repo.Run(ctx, "rev-parse", last.Tip+"^{tree}"); err != nil {
		return nil, err
	} else if tree != want {
		sha, err := wt.CommitTree(ctx, want, carryMessage)
		if err != nil {
			return nil, err
		}
		last.Tip = sha
		last.Commits = append(last.Commits, firstLine(carryMessage))
		res.Carried = true
	}
	if len(stack) == 1 {
		stack[0].Workstream = ""
	}
	for i := range stack {
		stack[i].Branch = prBranch(g, stack[i].Workstream)
	}
	stack[0].Branch = ReviewBranch(g)
	res.Stack = stack

	if opts.Gate != "" {
		if err := runGates(ctx, wt, res, opts); err != nil {
			return nil, err
		}
	}
	if err := updateBranches(ctx, repo, g, res); err != nil {
		return nil, err
	}
	res.Built = now(opts)
	// Where and how the goal lands outlives laying it out again.
	if old, err := Load(s.GoalDir(g.Name)); err == nil && old != nil && old.Landing != nil {
		res.Landing = &Landing{
			Remote: old.Landing.Remote,
			How:    old.Landing.How,
			PRs:    old.Landing.PRs,
		}
	}
	return res, Save(s.GoalDir(g.Name), res)
}

// expectedTree is the files the laid-out goal must end with: the
// integration branch's, merged with whatever the base branch gained since.
func expectedTree(
	ctx context.Context,
	repo git.Repo,
	g *queue.Goal,
	base, integration string,
) (string, error) {
	if on, err := repo.IsAncestor(ctx, base, integration); err != nil || on {
		if err != nil {
			return "", err
		}
		return repo.Run(ctx, "rev-parse", integration+"^{tree}")
	}
	tree, conflicted, err := repo.MergeTree(ctx, base, integration)
	if conflicted {
		return "", fmt.Errorf(
			"%s has moved on and conflicts with goal %s: finish it again, and an agent merges %s in",
			g.Base,
			g.Name,
			g.Base,
		)
	}
	return tree, err
}

// replayOnto replays the goal's commits onto base, one pull request per
// workstream, or when a workstream's commits don't apply on the ones before,
// as one pull request in the order they were made, saying why in res.
func replayOnto(
	ctx context.Context,
	wt git.Repo,
	base string,
	commits []commit,
	owner map[string]string,
	g *queue.Goal,
	res *Result,
) ([]PR, error) {
	stack, err := replay(ctx, wt, base, arrange(commits, owner, workstreamOrder(g)))
	if conflict := (*ConflictError)(nil); errors.As(err, &conflict) {
		res.Unstacked = conflict.Error()
		stack, err = replay(ctx, wt, base, arrange(commits, nil, []string{""}))
	}
	return stack, err
}

// commit is one of the goal's commits.
type commit struct {
	sha, subject string
}

// goalCommits lists the goal's commits that base lacks, oldest first,
// without the merges.
func goalCommits(ctx context.Context, repo git.Repo, base, integration string) ([]commit, error) {
	out, err := repo.Run(
		ctx,
		"log",
		"--reverse",
		"--topo-order",
		"--no-merges",
		"--format=%H%x1f%s",
		base+".."+integration,
	)
	if err != nil || out == "" {
		return nil, err
	}
	var commits []commit
	for line := range strings.SplitSeq(out, "\n") {
		sha, subject, _ := strings.Cut(line, "\x1f")
		commits = append(commits, commit{sha: sha, subject: subject})
	}
	return commits, nil
}

// owners maps each commit to the workstream it was made on: the workstream
// of the task that made it, or else the first workstream, in dependency
// order, that made it on its own branch.
func owners(
	ctx context.Context,
	s *queue.Store,
	g *queue.Goal,
	repo git.Repo,
	base string,
) (map[string]string, error) {
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return nil, err
	}
	owner := map[string]string{}
	for _, t := range tasks {
		for _, sha := range t.Commits {
			owner[sha] = t.Workstream
		}
	}
	for _, ws := range workstreamOrder(g)[1:] {
		branch := g.WorkstreamBranch(ws)
		if !repo.BranchExists(ctx, branch) {
			continue
		}
		out, err := repo.Run(ctx, "rev-list", "--first-parent", "--no-merges", base+".."+branch)
		if err != nil {
			return nil, err
		}
		for sha := range strings.SplitSeq(out, "\n") {
			if _, ok := owner[sha]; !ok && sha != "" {
				owner[sha] = ws
			}
		}
	}
	return owner, nil
}

// workstreamOrder is the order the pull requests land in: commits made on no
// workstream, such as the goal's ADRs, then each workstream after the ones
// it depends on, in the order the goal lists them.
func workstreamOrder(g *queue.Goal) []string {
	order := []string{""}
	placed := map[string]bool{}
	for len(placed) < len(g.Workstreams) {
		progress := false
		for _, ws := range g.Workstreams {
			if placed[ws.Name] {
				continue
			}
			ready := true
			for _, dep := range ws.DependsOn {
				if _, known := g.Workstream(dep); known && !placed[dep] {
					ready = false
				}
			}
			if ready {
				placed[ws.Name] = true
				order = append(order, ws.Name)
				progress = true
			}
		}
		if !progress { // a loop; the goal's order breaks it
			for _, ws := range g.Workstreams {
				if !placed[ws.Name] {
					placed[ws.Name] = true
					order = append(order, ws.Name)
				}
			}
		}
	}
	return order
}

// step is one commit replayed, with the fixups squashed into it.
type step struct {
	commit
	fixups []commit
}

// group is the commits of one pull request.
type group struct {
	ws    string
	steps []*step
}

// arrange sorts the commits into groups, in order, keeping the order they
// were made in within each. A fixup joins the commit it revises. Commits made
// on no workstream land with the first pull request. With no owners, every
// commit is in the first group.
func arrange(commits []commit, owner map[string]string, order []string) []group {
	groups := make([]group, len(order))
	index := map[string]int{}
	for i, ws := range order {
		groups[i].ws = ws
		index[ws] = i
	}
	var picked []*step
	for _, c := range commits {
		if target := fixupTarget(picked, c.subject); target != nil {
			target.fixups = append(target.fixups, c)
			continue
		}
		st := &step{commit: c}
		picked = append(picked, st)
		i := index[owner[c.sha]] // a workstream the goal no longer has is 0
		groups[i].steps = append(groups[i].steps, st)
	}
	var out []group
	var carry []*step
	for _, gr := range groups {
		gr.steps = append(carry, gr.steps...)
		carry = nil
		if len(gr.steps) == 0 {
			continue
		}
		if gr.ws == "" && len(order) > 1 {
			carry = gr.steps
			continue
		}
		out = append(out, gr)
	}
	if len(carry) > 0 {
		out = append(out, group{ws: "", steps: carry})
	}
	return out
}

// fixupTarget finds the commit a fixup revises among those picked before it:
// by SHA when the fixup names one, else the first commit with the subject.
func fixupTarget(picked []*step, subject string) *step {
	name, ok := strings.CutPrefix(subject, "fixup! ")
	if !ok {
		return nil
	}
	for {
		rest, more := strings.CutPrefix(name, "fixup! ")
		if !more {
			break
		}
		name = rest
	}
	if isHex(name) && len(name) >= 7 {
		for _, st := range picked {
			if strings.HasPrefix(st.sha, name) {
				return st
			}
		}
	}
	for _, st := range picked {
		if st.subject == name {
			return st
		}
	}
	return nil
}

// replay puts the groups' commits onto base, one pull request a group. A
// pick git can't finish is a ConflictError.
func replay(ctx context.Context, wt git.Repo, base string, groups []group) ([]PR, error) {
	if _, err := wt.Run(ctx, "checkout", "--detach", "--force", base); err != nil {
		return nil, err
	}
	var stack []PR
	for _, gr := range groups {
		start, err := wt.RevParse(ctx, "HEAD")
		if err != nil {
			return nil, err
		}
		pr := PR{Workstream: gr.ws}
		for _, st := range gr.steps {
			applied, err := apply(ctx, wt, st)
			if err != nil {
				return nil, err
			}
			pr.Commits = append(pr.Commits, applied...)
		}
		if pr.Tip, err = wt.RevParse(ctx, "HEAD"); err != nil {
			return nil, err
		}
		if pr.Tip != start {
			stack = append(stack, pr)
		}
	}
	return stack, nil
}

// replaySquashing replays steps onto base in order, as one pull request, up
// to the first git can't replay: it and every step after it are squashed into
// one commit of the files want. It returns the subjects of the commits
// squashed.
func replaySquashing(
	ctx context.Context,
	wt git.Repo,
	base string,
	steps []*step,
	want, baseName string,
) (PR, []string, error) {
	if _, err := wt.Run(ctx, "checkout", "--detach", "--force", base); err != nil {
		return PR{}, nil, err
	}
	var pr PR
	var squashed []string
	for i, st := range steps {
		before, err := wt.RevParse(ctx, "HEAD")
		if err != nil {
			return PR{}, nil, err
		}
		applied, err := apply(ctx, wt, st)
		if errors.As(err, new(*ConflictError)) {
			if _, err := wt.Run(ctx, "reset", "--quiet", "--hard", before); err != nil {
				return PR{}, nil, err
			}
			if squashed, err = squash(ctx, wt, steps[i:], want, baseName); err != nil {
				return PR{}, nil, err
			}
			if len(squashed) > 0 {
				pr.Commits = append(pr.Commits, st.subject)
			}
			break
		}
		if err != nil {
			return PR{}, nil, err
		}
		pr.Commits = append(pr.Commits, applied...)
	}
	tip, err := wt.RevParse(ctx, "HEAD")
	pr.Tip = tip
	return pr, squashed, err
}

// squash makes one commit of the files want on HEAD for steps, keeping each
// one's message as autosquash would, and returns their subjects. With HEAD
// already holding want, it makes nothing.
func squash(ctx context.Context, wt git.Repo, steps []*step, want, base string) ([]string,
	error,
) {
	if tree, err := wt.HeadTree(ctx); err != nil || tree == want {
		return nil, err
	}
	subjects := make([]string, 0, len(steps))
	msgs := make([]string, 0, len(steps))
	for _, st := range steps {
		subjects = append(subjects, st.subject)
		msg, err := wt.Output(ctx, "log", "-1", "--format=%B", st.sha)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, strings.TrimSpace(msg))
	}
	msg := fmt.Sprintf(squashMessage, steps[0].subject, base, base) + "\n" +
		strings.Join(msgs, "\n\n") + "\n"
	if _, err := wt.CommitTree(ctx, want, commitmsg.Wrap(msg)); err != nil {
		return nil, err
	}
	return subjects, nil
}

// apply replays one commit and squashes its fixups into it, and returns the
// subjects of the commits it made.
func apply(ctx context.Context, wt git.Repo, st *step) ([]string, error) {
	before, err := wt.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	if err := pick(ctx, wt, st.commit); err != nil {
		return nil, err
	}
	after, err := wt.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	if after == before {
		// The base already has the change; each fixup goes on its own.
		var subjects []string
		for _, f := range st.fixups {
			if err := pick(ctx, wt, f); err != nil {
				return nil, err
			}
			subjects = append(subjects, f.subject)
		}
		return subjects, nil
	}
	for _, f := range st.fixups {
		if _, err := wt.Run(ctx, git.Rerere("cherry-pick", "--no-commit", f.sha)...); err != nil {
			if err := settled(ctx, wt, f, err, "reset", "--hard", "HEAD"); err != nil {
				return nil, err
			}
			if _, err := wt.Run(ctx, "cherry-pick", "--quit"); err != nil {
				return nil, err
			}
		}
		if _, err := wt.Run(
			ctx,
			"commit",
			"--amend",
			"--no-edit",
			"--no-verify",
			"--allow-empty",
		); err != nil {
			return nil, err
		}
	}
	return []string{st.subject}, nil
}

// pick replays one commit, dropping it if the base already has it.
func pick(ctx context.Context, wt git.Repo, c commit) error {
	before, err := wt.RevParse(ctx, "HEAD")
	if err != nil {
		return err
	}
	if err := replayOne(ctx, wt, c); err != nil {
		return err
	}
	return rewrap(ctx, wt, before)
}

// rewrap wraps the message of the commit just replayed, when HEAD moved
// from before, at the width diatom writes: a commit made before diatom
// wrapped its messages, or before the repo's commitlint limited them, lands
// well formed all the same.
func rewrap(ctx context.Context, wt git.Repo, before string) error {
	head, err := wt.RevParse(ctx, "HEAD")
	if err != nil || head == before {
		return err
	}
	msg, err := wt.Output(ctx, "log", "-1", "--format=%B", "HEAD")
	if err != nil {
		return err
	}
	msg = strings.TrimRight(msg, "\n")
	if wrapped := commitmsg.Wrap(msg); wrapped != msg {
		return wt.Amend(ctx, wrapped+"\n")
	}
	return nil
}

// replayOne cherry-picks one commit. Git settles a conflict the catch-up
// merge already resolved, as reviewed, with the resolution it recorded.
func replayOne(ctx context.Context, wt git.Repo, c commit) error {
	_, err := wt.Run(ctx, git.Rerere("cherry-pick", "--allow-empty", "--empty=drop", c.sha)...)
	if err == nil {
		return nil
	}
	if err := settled(ctx, wt, c, err, "cherry-pick", "--abort"); err != nil {
		return err
	}
	// The commit goes on with its own message, or not at all when nothing
	// of it is left. It is committed without the repo's hooks, as every
	// commit of the layout is: the gate runs on the result.
	if _, err := wt.Run(ctx, "diff", "--cached", "--quiet", "HEAD"); err == nil {
		_, err = wt.Run(ctx, "cherry-pick", "--skip")
		return err
	}
	_, err = wt.Run(ctx, "commit", "--no-verify", "--no-edit")
	return err
}

// settled checks a pick of c that failed with err: nil when git settled
// every conflict it left with resolutions it recorded, such as the catch-up
// merge's as reviewed, so no path is left unmerged nor any conflict marker.
// Otherwise it undoes the pick, and a pick that left conflicts, which git
// marks with AUTO_MERGE, or a pick in progress with CHERRY_PICK_HEAD, is a
// ConflictError.
func settled(ctx context.Context, wt git.Repo, c commit, err error, undo ...string) error {
	_, aerr := wt.Run(ctx, "rev-parse", "--quiet", "--verify", "AUTO_MERGE")
	_, cerr := wt.Run(ctx, "rev-parse", "--quiet", "--verify", "CHERRY_PICK_HEAD")
	conflicted := aerr == nil || cerr == nil
	unmerged, uerr := wt.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if uerr != nil {
		return errors.Join(err, uerr)
	}
	if conflicted && unmerged == "" {
		markers, merr := wt.ConflictMarkers(ctx)
		if merr != nil || len(markers) == 0 {
			return merr
		}
		unmerged, _ = wt.Run(ctx, "diff", "--name-only", "HEAD")
	}
	if _, undoErr := wt.Run(ctx, undo...); undoErr != nil {
		return errors.Join(err, undoErr)
	}
	if !conflicted && unmerged == "" {
		return err
	}
	return &ConflictError{Commit: c.sha, Subject: c.subject, Files: strings.Split(unmerged, "\n")}
}

// runGates runs the gate on each pull request's tip, so a pull request that
// only passes with the ones after it shows before it is opened.
func runGates(ctx context.Context, wt git.Repo, res *Result, opts Options) error {
	run := gate.Within(opts.Timeout, opts.RunGate)
	if opts.RunGate == nil {
		gateRun := gate.Run
		if opts.Progress != nil {
			gateRun = gate.Echo(opts.Progress)
		}
		run = gate.Serial(gate.Within(opts.Timeout, gateRun))
	}
	for i := range res.Stack {
		pr := &res.Stack[i]
		opts.step("Running the gate on %s", pr.Branch)
		if _, err := wt.Run(ctx, "checkout", "--detach", "--force", pr.Tip); err != nil {
			return err
		}
		if _, err := wt.Run(ctx, "clean", "-fdq"); err != nil {
			return err
		}
		r, err := run(ctx, wt.Dir, opts.Gate)
		if err != nil {
			return err
		}
		pr.Gate = &Gate{Passed: r.Passed}
		if !r.Passed {
			pr.Gate.Output = r.Output
		}
	}
	return nil
}

// updateBranches points the final branch and each pull request's branch at
// the result, and deletes the branches of pull requests an earlier layout
// had and this one doesn't.
func updateBranches(ctx context.Context, repo git.Repo, g *queue.Goal, res *Result) error {
	keep := map[string]bool{}
	for _, pr := range res.Stack {
		keep["refs/heads/"+pr.Branch] = true
		if _, err := repo.Run(ctx, "update-ref", "refs/heads/"+pr.Branch, pr.Tip); err != nil {
			return err
		}
	}
	if _, err := repo.Run(ctx, "update-ref", "refs/heads/"+res.Final, res.Tip()); err != nil {
		return err
	}
	out, err := repo.Run(
		ctx,
		"for-each-ref",
		"--format=%(refname)",
		"refs/heads/"+prBranch(g, ""),
	)
	if err != nil {
		return err
	}
	for ref := range strings.SplitSeq(out, "\n") {
		if ref != "" && !keep[ref] {
			if _, err := repo.Run(ctx, "update-ref", "-d", ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// Current reports whether res still matches the goal's branches.
func Current(ctx context.Context, s *queue.Store, g *queue.Goal, res *Result) bool {
	repo := git.Repo{Dir: s.Repo()}
	base, err := repo.RevParse(ctx, BaseTip(ctx, repo, g))
	if err != nil {
		return false
	}
	integration, err := repo.RevParse(ctx, g.IntegrationBranch())
	if err != nil || base != res.Base || integration != res.Integration || len(res.Settled) > 0 {
		return false
	}
	return landable(ctx, repo, res)
}

// landable reports whether a layout's commits can land as they are: none a
// merge, which linear history refuses, and each signed when the repo signs
// its commits. A layout made before diatom kept to both is made again.
func landable(ctx context.Context, repo git.Repo, res *Result) bool {
	span := res.Base + ".." + res.Tip()
	if merges, err := repo.Run(ctx, "rev-list", "--merges", span); err != nil || merges != "" {
		return false
	}
	if sign, _ := repo.Run(ctx, "config", "--type=bool", "commit.gpgSign"); sign != "true" {
		return true
	}
	marks, err := repo.Run(ctx, "log", "--format=%G?", span)
	return err == nil && !strings.Contains(marks, "N")
}

// Save writes res to the goal's directory.
func Save(goalDir string, res *Result) error {
	data, err := yaml.Marshal(res)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(goalDir, fileName), data, 0o644)
}

// Load reads the goal's last layout, or nil when there is none.
func Load(goalDir string) (*Result, error) {
	data, err := os.ReadFile(filepath.Join(goalDir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var res Result
	if err := yaml.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("%s: %w", fileName, err)
	}
	if len(res.Stack) == 0 {
		return nil, fmt.Errorf("%s: no pull requests", fileName)
	}
	return &res, nil
}

func now(opts Options) time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

func isHex(s string) bool {
	return strings.Trim(s, "0123456789abcdef") == ""
}

func short(sha string) string { return sha[:min(len(sha), 7)] }

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
