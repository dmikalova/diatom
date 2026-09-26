package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
)

// applyReviews turns the human's review decisions into revisions (ADR 0001).
// The reviewer pane only writes decisions; this is the harness's half:
//
//   - A rejection joins the pending revision of its commit, created when
//     there is none. There is one revision per revised commit, so each lands
//     as that commit's fixup (ADR 0003); a workstream's revisions still run
//     together in one batch (ADR 0004).
//   - A changed rejection, such as a new comment, replaces its section while
//     the revision is pending.
//   - A rejection approved again before its revision is picked up is taken
//     back out, and an emptied revision is withdrawn. After pickup the change
//     is only recorded: the fixup comes back for review like any commit.
func (h *Harness) applyReviews(ctx context.Context, s *queue.Store, goal string) error {
	commits, err := reviewedCommits(s.GoalDir(goal))
	if err != nil || len(commits) == 0 {
		return err
	}
	tasks, err := s.Tasks(goal)
	if err != nil {
		return err
	}
	store := review.Store{Dir: s.GoalDir(goal)}
	repo := git.Repo{Dir: s.Repo()}
	for _, sha := range commits {
		rec, err := store.Load(sha)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(rec.Hunks))
		for id := range rec.Hunks {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if err := h.applyDecision(
				ctx,
				s,
				repo,
				goal,
				sha,
				id,
				rec.Hunks[id],
				&tasks,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// reviewedCommits lists the commits that have a review record.
func reviewedCommits(goalDir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(goalDir, "reviews", "*.yaml"))
	if err != nil {
		return nil, err
	}
	shas := make([]string, 0, len(files))
	for _, f := range files {
		shas = append(shas, strings.TrimSuffix(filepath.Base(f), ".yaml"))
	}
	return shas, nil
}

func (h *Harness) applyDecision(
	ctx context.Context,
	s *queue.Store,
	repo git.Repo,
	goal, sha, id string,
	rec *review.Record,
	tasks *[]*queue.Task,
) error {
	entry := fmt.Sprintf("%s@%d", id, rec.Seq)
	var pending *queue.Task
	for _, t := range *tasks {
		if t.Kind != queue.Revision || t.Revises != sha {
			continue
		}
		if slices.Contains(t.Hunks, entry) {
			return nil // already carried at this version
		}
		if t.State == queue.Pending {
			pending = t
		}
	}

	if rec.Decision != review.Reject {
		if pending == nil || !carries(pending, id) {
			return nil
		}
		dropHunk(pending, id)
		if len(pending.Hunks) > 0 {
			return s.SaveTask(goal, pending)
		}
		pending.Body = appendSection(
			pending.Body,
			"Withdrawn",
			"Every rejection this revision carried was approved "+
				"again before it started, so there is nothing left to revise.",
		)
		return s.Move(goal, pending, queue.Done)
	}

	hunk, err := findHunk(ctx, repo, sha, id)
	if err != nil || hunk == nil {
		return err
	}
	section := hunkSection(*hunk, rec)
	if pending != nil {
		dropHunk(pending, id)
		pending.Hunks = append(pending.Hunks, entry)
		pending.Body = strings.TrimRight(pending.Body, "\n") + "\n\n" + section
		return s.SaveTask(goal, pending)
	}

	origin := originTasks(*tasks, sha)
	if len(origin) == 0 {
		h.log().Warn("a rejected hunk's commit was made by no task", "goal", goal, "commit", sha)
		return nil
	}
	subject, err := repo.Subject(ctx, sha)
	if err != nil {
		return err
	}
	t := &queue.Task{
		Title:      fmt.Sprintf("Revise %s: %s", sha[:min(7, len(sha))], subject),
		Kind:       queue.Revision,
		Workstream: origin[0].Workstream,
		Origin:     queue.Origin{Type: "review", Ref: sha},
		Revises:    sha,
		Hunks:      []string{entry},
		Created:    h.now(),
		Body:       revisionIntro(sha, subject, origin) + "\n\n" + section,
	}
	if err := s.AddTask(goal, t); err != nil {
		return err
	}
	*tasks = append(*tasks, t)
	return nil
}

// originTasks are the tasks whose sessions made sha, planned work first.
func originTasks(tasks []*queue.Task, sha string) []*queue.Task {
	var out []*queue.Task
	for _, t := range tasks {
		if slices.Contains(t.Commits, sha) {
			out = append(out, t)
		}
	}
	slices.SortStableFunc(out, func(a, b *queue.Task) int {
		return boolRank(a.Kind == queue.Revision) - boolRank(b.Kind == queue.Revision)
	})
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func findHunk(ctx context.Context, repo git.Repo, sha, id string) (*review.Hunk, error) {
	hunks, err := review.Hunks(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	for i := range hunks {
		if hunks[i].ID == id {
			return &hunks[i], nil
		}
	}
	return nil, nil
}

func revisionIntro(sha, subject string, origin []*queue.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The human rejected parts of commit %s (%q). Rework the code it introduced, "+
		"following the review comments on each hunk below. diatom commits the change as a fixup of that commit.\n\n"+
		"The commit was made for:\n", sha[:min(7, len(sha))], subject)
	for _, t := range origin {
		fmt.Fprintf(&b, "\n- Task %s: %s", t.ID, t.Title)
	}
	return b.String()
}

// hunkSection renders one rejected hunk with its comments, between markers
// that let a later change replace or remove it.
func hunkSection(h review.Hunk, rec *review.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- diatom:hunk %s -->\n### `%s`, hunk %d of %d\n\n```diff\n%s\n```\n\n",
		h.ID, h.Path, h.Index, h.Of, h.Text())
	if len(rec.Comments) == 0 {
		b.WriteString("- The human rejected this hunk as a whole, without a comment.\n")
	}
	for _, c := range rec.Comments {
		line := ""
		if c.Line >= 0 && c.Line < len(h.Fragment.Lines) {
			line = " (`" + strings.TrimRight(h.Fragment.Lines[c.Line].String(), "\n") + "`)"
		}
		fmt.Fprintf(&b, "- Line %d%s: %s\n", h.NewLine(c.Line), line, c.Text)
	}
	fmt.Fprintf(&b, "<!-- /diatom:hunk %s -->\n", h.ID)
	return b.String()
}

func carries(t *queue.Task, id string) bool {
	return slices.ContainsFunc(t.Hunks, func(e string) bool { return strings.HasPrefix(e, id+"@") })
}

// dropHunk removes a hunk's entry and section from a revision.
func dropHunk(t *queue.Task, id string) {
	t.Hunks = slices.DeleteFunc(
		t.Hunks,
		func(e string) bool { return strings.HasPrefix(e, id+"@") },
	)
	re := regexp.MustCompile(
		`(?s)\n*<!-- diatom:hunk ` + regexp.QuoteMeta(id) + ` -->.*?<!-- /diatom:hunk ` +
			regexp.QuoteMeta(id) + ` -->\n?`,
	)
	t.Body = strings.TrimRight(re.ReplaceAllString(t.Body, "\n"), "\n") + "\n"
}

// commitFixups commits a revision batch's work as one fixup per revised
// commit (ADR 0003). The task tool snapshotted the files each time a revision
// was marked done, so the work splits at those points; whatever changed after
// the last one goes with it. The gate ran on the final files: a fixup's own
// tree is never merged anywhere, and autosquash moves it beside its original
// anyway.
func (h *Harness) commitFixups(ctx context.Context, wt git.Repo, base string, tasks []*queue.Task,
	finished []sessionDone,
) (map[string]string, error) {
	final, err := wt.WriteTree(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*queue.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	// A revision marked done twice splits at its last mark.
	var order []sessionDone
	for _, f := range finished {
		if byID[f.task] == nil {
			continue
		}
		order = slices.DeleteFunc(order, func(o sessionDone) bool { return o.task == f.task })
		order = append(order, f)
	}
	if len(order) == 0 {
		// Unfinished work still belongs to a revision: the first one's.
		order = []sessionDone{{task: tasks[0].ID}}
	}
	order[len(order)-1].tree = final

	shas := map[string]string{}
	for _, f := range order {
		head, err := wt.HeadTree(ctx)
		if err != nil {
			return nil, err
		}
		if f.tree == "" || f.tree == head {
			continue
		}
		target, err := fixupTarget(ctx, wt, base, byID[f.task].Revises)
		if err != nil {
			return nil, err
		}
		sha, err := wt.CommitTree(ctx, f.tree, "fixup! "+target+"\n")
		if err != nil {
			return nil, err
		}
		shas[f.task] = sha
	}
	// The index already holds the final files, which are now HEAD's.
	return shas, nil
}

// fixupTarget names the revised commit in a fixup's subject: by its subject,
// as ADR 0003 has it, unless another commit on the goal's branch shares that
// subject, when autosquash could pick the wrong one; then by its SHA, which
// autosquash also accepts.
func fixupTarget(ctx context.Context, wt git.Repo, base, sha string) (string, error) {
	subject, err := wt.Subject(ctx, sha)
	if err != nil {
		return "", err
	}
	log, err := wt.Run(ctx, "log", "--format=%s", base+"..HEAD")
	if err != nil {
		return "", err
	}
	same := 0
	for line := range strings.SplitSeq(log, "\n") {
		if line == subject {
			same++
		}
	}
	if same > 1 {
		return sha, nil
	}
	return subject, nil
}

// sessionDone is one revision marked done, with the files at that moment.
type sessionDone struct {
	task, tree string
}
