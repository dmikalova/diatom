package ui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestReviewSendsItsImages(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")
	f := newFixture(t)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.repo, "dot.png"), buf.String())
	r := git.Repo{Dir: f.repo}
	if _, err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "feat: a dot")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddTask("set", &queue.Task{Title: "Draw", Kind: queue.Planned,
		Workstream: "art", Commits: []string{sha}}); err != nil {
		t.Fatal(err)
	}
	a, _ := newApp(t, f)
	openGoal(t, a, "set")
	a.openReview(a.env.Store, "set")
	// Past the ward's hunk to the dot's.
	a.review.Key(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if seq := a.images(); !strings.Contains(seq, "\x1b_Ga=T,U=1") {
		t.Fatalf("the dot wasn't sent: %q", seq)
	}
	if !strings.Contains(a.frame, "\U0010EEEE") {
		t.Error("the frame doesn't hold the dot")
	}
	if _, cmd := a.Update(tickMsg{}); cmd == nil {
		t.Error("no tick")
	}
	if exit() == nil {
		t.Error("no exit")
	}
}
