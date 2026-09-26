package review

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

type fixture struct {
	t     *testing.T
	repo  git.Repo
	store *queue.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	r := git.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"config", "user.name", "T"}, {"config", "user.email", "t@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, repo: r, store: queue.Open(r.Dir)}
	f.write("ward.go", lines(1, 20))
	f.commit("chore: start")
	if err := f.store.CreateGoal(&queue.Goal{Name: "set", State: queue.GoalActive}); err != nil {
		t.Fatal(err)
	}
	return f
}

func lines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		b.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	return b.String()
}

func (f *fixture) write(name, body string) {
	f.t.Helper()
	path := filepath.Join(f.repo.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(msg string) string {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.repo.StageAll(ctx); err != nil {
		f.t.Fatal(err)
	}
	sha, err := f.repo.Commit(ctx, msg)
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) task(kind queue.Kind, commits ...string) *queue.Task {
	f.t.Helper()
	task := &queue.Task{Title: "t", Kind: kind, Workstream: "engine", Commits: commits}
	if err := f.store.AddTask("set", task); err != nil {
		f.t.Fatal(err)
	}
	return task
}

// twoHunks edits the start and end of ward.go, far enough apart to make two
// hunks, and adds a file.
func (f *fixture) twoHunks() string {
	body := strings.Replace(lines(1, 20), "line b\n", "line B\n", 1)
	body = strings.Replace(body, "line u\n", "line U\n", 1)
	f.write("ward.go", body)
	f.write("new.go", "package x\n")
	return f.commit("feat: ward")
}

func TestHunks(t *testing.T) {
	f := newFixture(t)
	sha := f.twoHunks()
	hunks, err := Hunks(context.Background(), f.repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, h := range hunks {
		ids = append(ids, h.ID)
	}
	if strings.Join(ids, " ") != "new.go#1 ward.go#1 ward.go#2" {
		t.Fatalf("hunk IDs = %v", ids)
	}
	if !hunks[0].New || hunks[1].Of != 2 || hunks[1].Commit != sha {
		t.Errorf("hunks = %+v", hunks)
	}
	text := hunks[1].Text()
	if !strings.HasPrefix(text, "@@ -1,4 +1,4 @@") || !strings.Contains(text, "-line b\n+line B") {
		t.Errorf("Text = %q", text)
	}
	// The fragment's lines are: the deleted old line 1, the added new line 1,
	// then context from line 2.
	for i, want := range []int{1, 1, 2} {
		if got := hunks[1].NewLine(i); got != want {
			t.Errorf("NewLine(%d) = %d, want %d", i, got, want)
		}
	}
}

func TestHunksOfMerge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.repo.Run(ctx, "checkout", "-q", "-b", "other"); err != nil {
		t.Fatal(err)
	}
	f.write("ward.go", strings.Replace(lines(1, 20), "line b\n", "line OTHER\n", 1))
	f.commit("feat: other")
	if _, err := f.repo.Run(ctx, "checkout", "-q", "main"); err != nil {
		t.Fatal(err)
	}
	f.write("ward.go", strings.Replace(lines(1, 20), "line b\n", "line MAIN\n", 1))
	f.commit("feat: main")
	if _, err := f.repo.MergeNoCommit(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	f.write("ward.go", strings.Replace(lines(1, 20), "line b\n", "line BOTH\n", 1))
	merge, err := f.repo.CommitMerge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hunks, err := Hunks(ctx, f.repo, merge)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || !strings.Contains(hunks[0].Text(), "+line BOTH") ||
		!strings.Contains(hunks[0].Text(), "-<<<<<<<") {
		t.Errorf("merge hunks = %+v, want the resolution against the conflict", hunks)
	}
}

func TestDecideAndQueue(t *testing.T) {
	f := newFixture(t)
	first := f.twoHunks()
	f.write("later.go", "package later\n")
	second := f.commit("feat: later")
	f.task(queue.Planned, first)
	f.task(queue.Planned, second)
	ctx := context.Background()

	items, err := Load(ctx, f.store, "set")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || items[0].Commit != first || items[3].Commit != second ||
		items[0].Subject != "feat: ward" {
		t.Fatalf("items = %+v", items)
	}

	s := Store{Dir: f.store.GoalDir("set")}
	now := time.Unix(100, 0).UTC()
	if _, err := s.Decide(items[0].Hunk, Defer, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(
		items[1].Hunk,
		Reject,
		[]Comment{{Line: 1, Text: "why?"}},
		now,
	); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Decide(items[1].Hunk, Reject, []Comment{{Line: 1, Text: "why not B?"}}, now)
	if err != nil || rec.Seq != 2 {
		t.Fatalf("second decision = %+v, %v", rec, err)
	}

	items, _ = Load(ctx, f.store, "set")
	pending := Pending(items)
	if len(pending) != 3 || pending[0].ID != "ward.go#2" || pending[2].ID != "new.go#1" {
		var ids []string
		for _, p := range pending {
			ids = append(ids, p.ID)
		}
		t.Errorf("pending = %v, want the unreviewed then the deferred", ids)
	}
	counts := Counts(items)
	if counts[""] != 2 || counts[Defer] != 1 || counts[Reject] != 1 {
		t.Errorf("counts = %v", counts)
	}
	if hist, _ := s.History(); len(hist) != 3 || hist[2].Hunk != "ward.go#1" {
		t.Errorf("history = %+v", hist)
	}
}

func TestApprovedCommentBecomesIntake(t *testing.T) {
	f := newFixture(t)
	sha := f.twoHunks()
	hunks, _ := Hunks(context.Background(), f.repo, sha)
	s := Store{Dir: f.store.GoalDir("set")}
	if _, err := s.Decide(
		hunks[1],
		Approve,
		[]Comment{{Line: 2, Text: "later, rename B"}},
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "intake"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("intake = %v, %v", entries, err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "intake", entries[0].Name()))
	if !bytes.Contains(b, []byte("Line 2: later, rename B")) ||
		!bytes.Contains(b, []byte("hunk: ward.go#1")) {
		t.Errorf("intake = %s", b)
	}
}

func TestCombined(t *testing.T) {
	f := newFixture(t)
	original := f.twoHunks()
	// An unrelated commit between the original and its fixup.
	f.write("other.go", "package other\n")
	f.commit("feat: other")
	body := strings.Replace(lines(1, 20), "line b\n", "line Bee\n", 1)
	f.write("ward.go", strings.Replace(body, "line u\n", "line U\n", 1))
	fixup := f.commit("fixup! feat: ward")

	hunks, err := Combined(context.Background(), f.repo, original, fixup)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, h := range hunks {
		text.WriteString(h.ID + "\n" + h.Text() + "\n")
	}
	got := text.String()
	if !strings.Contains(got, "+line Bee") || strings.Contains(got, "+line B\n") ||
		strings.Contains(got, "other.go") {
		t.Errorf(
			"combined =\n%s\nwant the fix folded in and nothing from the unrelated commit",
			got,
		)
	}
}

func TestLoadMissingReview(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	c, err := s.Load("abc")
	if err != nil || len(c.Hunks) != 0 {
		t.Errorf("Load = %+v, %v", c, err)
	}
	if hist, err := s.History(); err != nil || hist != nil {
		t.Errorf("History = %v, %v", hist, err)
	}
}
