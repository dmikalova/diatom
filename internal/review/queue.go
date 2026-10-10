package review

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dmikalova/diatom/internal/git"
	"github.com/dmikalova/diatom/internal/queue"
)

// Item is one hunk in a goal's review, with what the reviewer shows beside it.
type Item struct {
	Hunk
	// Subject is the commit's subject line.
	Subject string
	// Record is the decision on the hunk, or nil while it is unreviewed.
	Record *Record
	// Tasks are the tasks whose sessions made the commit.
	Tasks []*queue.Task
	// Revision is the revision task that made the commit, when it is a
	// fixup; its text holds the comments that caused it.
	Revision *queue.Task
}

// Load builds a goal's review: every hunk of every commit its tasks made,
// oldest commit first.
func Load(ctx context.Context, s *queue.Store, goal string) ([]Item, error) {
	tasks, err := s.Tasks(goal)
	if err != nil {
		return nil, err
	}
	byCommit := map[string][]*queue.Task{}
	var shas []string
	for _, t := range tasks {
		if t.SettledLanding() {
			continue
		}
		for _, sha := range t.Commits {
			if _, ok := byCommit[sha]; !ok {
				shas = append(shas, sha)
			}
			byCommit[sha] = append(byCommit[sha], t)
		}
	}
	repo := git.Repo{Dir: s.Repo()}
	commits := make([]commit, 0, len(shas))
	for _, sha := range shas {
		c, err := loadCommit(ctx, repo, sha)
		if err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	slices.SortStableFunc(commits, func(a, b commit) int { return cmp.Compare(a.time, b.time) })

	store := Store{Dir: s.GoalDir(goal)}
	var items []Item
	for _, c := range commits {
		rec, err := store.Load(c.sha)
		if err != nil {
			return nil, err
		}
		var revision *queue.Task
		for _, t := range byCommit[c.sha] {
			if t.Kind == queue.Revision {
				revision = t
			}
		}
		for _, h := range c.hunks {
			items = append(items, Item{
				Hunk: h, Subject: c.subject, Record: rec.Hunks[h.ID],
				Tasks: byCommit[c.sha], Revision: revision,
			})
		}
	}
	return items, nil
}

// commit is what Load reads of a commit.
type commit struct {
	sha, subject string
	time         int64
	hunks        []Hunk
}

// commits holds each commit Load has read, by repo and SHA. A commit never
// changes, so the review reloading after each decision reads only the
// decisions again, not every commit's diff.
var commits sync.Map

func loadCommit(ctx context.Context, repo git.Repo, sha string) (commit, error) {
	key := repo.Dir + "\x00" + sha
	if v, ok := commits.Load(key); ok {
		if c, ok := v.(commit); ok {
			return c, nil
		}
	}
	out, err := repo.Run(ctx, "show", "-s", "--format=%ct%x00%s", sha)
	if err != nil {
		return commit{}, err
	}
	ts, subject, _ := strings.Cut(out, "\x00")
	t, _ := strconv.ParseInt(ts, 10, 64)
	hunks, err := Hunks(ctx, repo, sha)
	if err != nil {
		return commit{}, err
	}
	c := commit{sha: sha, subject: subject, time: t, hunks: hunks}
	commits.Store(key, c)
	return c, nil
}

// Pending returns the items left to review: every unreviewed hunk, then the
// deferred ones, each in commit order (ADR 0001).
func Pending(items []Item) []Item {
	var unreviewed, deferred []Item
	for _, it := range items {
		switch {
		case it.Record == nil:
			unreviewed = append(unreviewed, it)
		case it.Record.Decision == Defer:
			deferred = append(deferred, it)
		}
	}
	return append(unreviewed, deferred...)
}

// Counts tallies a goal's hunks by decision, with "" for unreviewed.
func Counts(items []Item) map[Decision]int {
	counts := map[Decision]int{}
	for _, it := range items {
		if it.Record == nil {
			counts[""]++
		} else {
			counts[it.Record.Decision]++
		}
	}
	return counts
}
