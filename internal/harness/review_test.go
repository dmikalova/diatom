package harness

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/session"
)

// done marks a task done the way the task tool does, snapshotting the
// worktree in a revision session.
func (s agentSession) done(task string) {
	s.t.Helper()
	e := session.Entry{Type: session.EntryDone, Task: task}
	if s.spec.Kind == queue.Revision {
		tree, err := git.Repo{Dir: s.spec.Worktree}.Fingerprint(context.Background())
		if err != nil {
			s.t.Fatal(err)
		}
		e.Tree = tree
	}
	if err := session.Append(s.dir, s.spec, e); err != nil {
		s.t.Fatal(err)
	}
}

// landTwoCommits runs two planned tasks in engine, each in its own session
// and so its own commit, touching ward.txt and poison.txt.
func (f *fixture) landTwoCommits(t *testing.T) (ward, poison string) {
	t.Helper()
	for _, name := range []string{"ward", "poison"} {
		task := f.add("engine", "Add "+name)
		f.agent.act = func(t *testing.T, wt string, s agentSession) {
			writeFile(t, wt, name+".txt", name+" v1\n")
			s.done(task.ID)
		}
		f.step()
	}
	ctx := context.Background()
	engine := git.Repo{Dir: f.store.WorktreeDir("set", "engine")}
	var err error
	if poison, err = engine.RevParse(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if ward, err = engine.RevParse(ctx, "HEAD~1"); err != nil {
		t.Fatal(err)
	}
	return ward, poison
}

func (f *fixture) reject(t *testing.T, sha, comment string) review.Hunk {
	t.Helper()
	hunks, err := review.Hunks(context.Background(), f.main, sha)
	if err != nil || len(hunks) != 1 {
		t.Fatalf("hunks of %s = %v, %v", sha, hunks, err)
	}
	if _, err := (review.Store{Dir: f.store.GoalDir("set")}).Decide(hunks[0], review.Reject,
		[]review.Comment{{Line: 0, Text: comment}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return hunks[0]
}

func (f *fixture) revisions(t *testing.T) []*queue.Task {
	t.Helper()
	tasks, err := f.store.Tasks("set")
	if err != nil {
		t.Fatal(err)
	}
	var out []*queue.Task
	for _, task := range tasks {
		if task.Kind == queue.Revision {
			out = append(out, task)
		}
	}
	return out
}

func TestRejectionsLandAsFixups(t *testing.T) {
	f := newFixture(t)
	ward, poison := f.landTwoCommits(t)
	f.reject(t, ward, "call it Ward")
	f.reject(t, poison, "poison needs a counter")

	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		if s.spec.Kind != queue.Revision || len(s.spec.Tasks) != 2 {
			t.Errorf(
				"session = %s with %v, want both revisions in one batch",
				s.spec.Kind,
				s.spec.Tasks,
			)
		}
		for _, id := range s.spec.Tasks {
			task, _ := f.store.Task("set", id)
			name := "ward"
			if strings.Contains(task.Body, "poison") {
				name = "poison"
			}
			if !strings.Contains(task.Body, "+"+name+" v1") {
				t.Errorf("revision %s lacks its hunk:\n%s", id, task.Body)
			}
			writeFile(t, wt, name+".txt", name+" v2\n")
			s.done(id)
		}
	}
	if got := f.step(); len(got) != 1 || got[0].Kind != queue.Revision {
		t.Fatalf("step = %+v, want one revision batch", got)
	}

	ctx := context.Background()
	log, err := f.main.Run(ctx, "log", "--format=%s", "-2", "diatom/set/integration")
	if err != nil {
		t.Fatal(err)
	}
	subjects := strings.Split(log, "\n")
	if len(subjects) != 2 || !strings.HasPrefix(subjects[0], "fixup! ") ||
		!strings.HasPrefix(subjects[1], "fixup! ") ||
		subjects[0] == subjects[1] {
		t.Fatalf("latest subjects = %q, want two different fixups", subjects)
	}
	for _, rev := range f.revisions(t) {
		if rev.State != queue.Done || len(rev.Commits) != 1 {
			t.Fatalf("revision = %+v", rev)
		}
		files, _ := f.main.Run(
			ctx,
			"diff-tree",
			"--no-commit-id",
			"--name-only",
			"-r",
			rev.Commits[0],
		)
		subject, _ := f.main.Subject(ctx, rev.Commits[0])
		// Both originals share a subject, so each fixup names its by SHA.
		if subject != "fixup! "+rev.Revises || strings.Contains(files, "\n") {
			t.Errorf(
				"fixup %s = %q touching %q, want only its own revision's file",
				rev.Commits[0],
				subject,
				files,
			)
		}
	}
}

func TestApprovalBeforePickupWithdraws(t *testing.T) {
	f := newFixture(t)
	ward, _ := f.landTwoCommits(t)
	h := f.reject(t, ward, "call it Ward")
	ctx := context.Background()
	if err := f.h.applyReviews(ctx, f.store, "set"); err != nil {
		t.Fatal(err)
	}
	revs := f.revisions(t)
	if len(revs) != 1 || revs[0].State != queue.Pending ||
		!strings.Contains(revs[0].Body, "call it Ward") {
		t.Fatalf("revisions = %+v", revs)
	}

	// A changed comment replaces the section while the revision waits.
	store := review.Store{Dir: f.store.GoalDir("set")}
	if _, err := store.Decide(
		h,
		review.Reject,
		[]review.Comment{{Line: 0, Text: "call it Warden"}},
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := f.h.applyReviews(ctx, f.store, "set"); err != nil {
		t.Fatal(err)
	}
	rev, _ := f.store.Task("set", revs[0].ID)
	replaced := !strings.Contains(rev.Body, "call it Ward\n") &&
		strings.Contains(rev.Body, "call it Warden")
	if !replaced || strings.Count(rev.Body, "<!-- diatom:hunk ward.txt#1 -->") != 1 {
		t.Fatalf("revision after a new comment:\n%s", rev.Body)
	}
	if len(rev.Hunks) != 1 || rev.Hunks[0] != "ward.txt#1@2" {
		t.Fatalf("revision hunks after a new comment = %v", rev.Hunks)
	}

	if _, err := store.Decide(h, review.Approve, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := f.step(); len(got) != 0 {
		t.Errorf("a withdrawn revision was scheduled: %+v", got)
	}
	rev, _ = f.store.Task("set", revs[0].ID)
	if rev.State != queue.Done || !strings.Contains(rev.Body, "## Withdrawn") ||
		len(rev.Hunks) != 0 {
		t.Errorf("revision after approval = %s %v\n%s", rev.State, rev.Hunks, rev.Body)
	}
}

func TestRejectionAfterPickupIsNewWork(t *testing.T) {
	f := newFixture(t)
	ward, _ := f.landTwoCommits(t)
	h := f.reject(t, ward, "call it Ward")
	f.agent.act = func(t *testing.T, wt string, s agentSession) {
		writeFile(t, wt, "ward.txt", "ward v2\n")
		s.done(s.spec.Tasks[0])
	}
	f.step()
	if revs := f.revisions(t); len(revs) != 1 || revs[0].State != queue.Done {
		t.Fatalf("revisions = %+v", revs)
	}
	// Rejecting the same hunk again, with a new comment, needs a new revision.
	if _, err := (review.Store{Dir: f.store.GoalDir("set")}).Decide(h, review.Reject,
		[]review.Comment{{Line: 0, Text: "still wrong"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.applyReviews(context.Background(), f.store, "set"); err != nil {
		t.Fatal(err)
	}
	if revs := f.revisions(
		t,
	); len(revs) != 2 || revs[1].State != queue.Pending ||
		revs[1].Revises != ward {
		t.Errorf("revisions = %+v, want a second one for the same commit", revs)
	}
}

func TestFixupTarget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.main.Run(ctx, "checkout", "-q", "-b", "work"); err != nil {
		t.Fatal(err)
	}
	var shas []string
	for _, msg := range []string{"feat: ward", "feat: poison", "feat: poison"} {
		writeFile(t, f.main.Dir, "f.txt", msg+time.Now().String())
		if _, err := f.main.StageAll(ctx); err != nil {
			t.Fatal(err)
		}
		sha, err := f.main.Commit(ctx, msg)
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, sha)
	}
	if got, _ := fixupTarget(ctx, f.main, "main", shas[0]); got != "feat: ward" {
		t.Errorf("unique subject target = %q", got)
	}
	if got, _ := fixupTarget(ctx, f.main, "main", shas[1]); got != shas[1] {
		t.Errorf("shared subject target = %q, want the SHA", got)
	}
}
