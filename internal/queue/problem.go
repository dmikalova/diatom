package queue

import "fmt"

// Fix is one thing the human may do about a problem beyond the three every
// problem offers. A fix either records Set on the problem, or, when Value is
// set, what the human types.
type Fix struct {
	Key   string
	Label string
	Set   string
	Value bool
}

// The keys for the three resolutions every problem has, which a kind's own
// fixes avoid.
const (
	RetryKey  = "r"
	GiveUpKey = "g"
	ReplyKey  = "c"
)

// ReopenFix is the Fix that says to make a fresh pull request rather than
// point the goal at one that already exists. DeleteFix says to throw the
// thing the problem is about away.
const (
	ReopenFix = "reopen"
	DeleteFix = "delete"
)

// Fixes are what the human may do about p beyond retrying it, giving up on
// it and replying to it (ADR 0014).
func (p *Problem) Fixes() []Fix {
	switch p.Kind {
	case ProblemPRClosed:
		return []Fix{
			{Key: "u", Label: "Its pull request moved: paste the new URL", Value: true},
			{Key: "o", Label: "Open another pull request for it", Set: ReopenFix},
		}
	case ProblemWorktree:
		return []Fix{
			{Key: "u", Label: "Its repo moved: give the path it is at now", Value: true},
			{Key: "x", Label: "Its repo is gone: forget its goals too", Set: DeleteFix},
		}
	case ProblemRepo, ProblemCheckout:
		// Nothing of this repo's is diatom's to throw away: it kept none,
		// which is the problem. The human fixes the repo, or gives up on it.
		return nil
	case ProblemConfigKey:
		return []Fix{{Key: "x", Label: "Delete the key from the config", Set: DeleteFix}}
	}
	return nil
}

// Hint is what to type for the fix that takes a value.
func (p *Problem) Hint() string {
	switch p.Kind {
	case ProblemPRClosed:
		return "The URL of the pull request that replaces it."
	case ProblemWorktree:
		return "The path its repo is at now."
	}
	return "The value it needs."
}

// Summary is the one line the nav shows for an open problem.
func (p *Problem) Summary() string {
	if p.Count > 1 {
		return fmt.Sprintf("%s (%d times)", p.What, p.Count)
	}
	return p.What
}
