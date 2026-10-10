package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/tui"
)

// retryKey closes a problem so its goal runs again, giveUpKey drops the
// goal, and replyKey types what the agent should do about it.
const (
	retryKey  = queue.RetryKey
	giveUpKey = queue.GiveUpKey
	replyKey  = queue.ReplyKey
)

// problemTail is how much of a problem's output shows in Next. The whole of
// it is in the file, which the editor opens.
const problemTail = 30

// replyHint is what the reply box asks for.
const replyHint = "Say what to do about it; the goal gets it as a task."

// problemActions are what can be done about a problem: try again, give up on
// the goal, reply to it, whatever its kind knows, and putting it off.
func problemActions(it item) []action {
	acts := []action{
		{retryKey, "Try again: it was a blip, run the goal as it was"},
		{giveUpKey, "Give up: drop the goal"},
		{replyKey, "Reply: tell the agent what to do about it"},
	}
	for _, f := range it.problem.Fixes() {
		acts = append(acts, action{f.Key, f.Label})
	}
	return append(acts, action{laterKey, "Later: ask again once the rest are done"})
}

// problemKey moves through a problem's actions, and does one on enter or on
// its own key.
func (n *Next) problemKey(it item, k string) tea.Cmd {
	acts := problemActions(it)
	chose := func(key string) bool {
		return k == key || (k == "enter" || k == "space" || k == " ") && n.act < len(acts) &&
			acts[n.act].key == key
	}
	switch k {
	case "esc":
		return n.setArea(areaBody)
	case "j", "down":
		n.act = min(n.act+1, max(len(acts)-1, 0))
		return nil
	case "k", "up":
		n.act = max(n.act-1, 0)
		return nil
	}
	switch {
	case chose(laterKey):
		return n.putOff(it)
	case chose(replyKey):
		n.more, n.fix = true, ""
		n.answer.Reset()
		return n.answer.Focus()
	case chose(retryKey):
		return n.doProblem(it, func(p *queue.Problem) error {
			return it.row.store.CloseProblem(it.row.goal.Name, p)
		}, it.row.goal.Name+" runs again")
	case chose(giveUpKey):
		if n.confirm != it.id() {
			n.confirm = it.id()
			n.flash = giveUpKey + " again to drop " + it.row.goal.Name
			return nil
		}
		return n.giveUp(it)
	}
	for _, f := range it.problem.Fixes() {
		if !chose(f.Key) {
			continue
		}
		if f.Value {
			n.more, n.fix = true, f.Key
			n.answer.Reset()
			return n.answer.Focus()
		}
		return n.setFix(it, f.Set)
	}
	return nil
}

// setFix records what the human chose on the problem. The harness carries it
// out and closes the problem.
func (n *Next) setFix(it item, value string) tea.Cmd {
	return n.doProblem(it, func(p *queue.Problem) error {
		p.Fix = value
		return it.row.store.SaveProblem(it.row.goal.Name, p)
	}, it.row.goal.Name+" has what it needs to go on")
}

// doProblem writes a change to the problem and moves on.
func (n *Next) doProblem(it item, write func(*queue.Problem) error, said string) tea.Cmd {
	if err := write(it.problem); err != nil {
		n.err = err
		return nil
	}
	n.flash = said
	return n.moveOn()
}

// giveUp drops the goal a problem is on, and closes the problem with it.
func (n *Next) giveUp(it item) tea.Cmd {
	g := it.row.goal
	g.State = queue.GoalDropped
	if err := it.row.store.SaveGoal(g); err != nil {
		n.err = err
		return nil
	}
	if err := it.row.store.CloseProblem(g.Name, it.problem); err != nil {
		n.err = err
		return nil
	}
	n.flash = g.Name + " is dropped"
	return n.moveOn()
}

// replyText types a reply to a problem, or the value its fix asked for, and
// records it once enter sends it.
func (n *Next) replyText(it item, msg tea.KeyPressMsg) nextKey {
	if cmd, ok := tui.Cut(&n.answer, msg); ok {
		return nextKey{cmd: cmd}
	}
	switch msg.String() {
	case "esc":
		n.more, n.fix = false, ""
		n.answer.Reset()
		n.answer.Blur()
		return nextKey{}
	case "enter":
		text := strings.TrimSpace(n.answer.Value())
		if text == "" {
			n.flash = "say what to do about it, or esc goes back"
			return nextKey{}
		}
		if n.fix != "" {
			return nextKey{cmd: n.setFix(it, text)}
		}
		return nextKey{cmd: n.doProblem(it, func(p *queue.Problem) error {
			p.Reply, p.Replied = text, n.env.Now()
			return it.row.store.SaveProblem(it.row.goal.Name, p)
		}, "the agent gets it as a task of "+it.row.goal.Name+"'s")}
	}
	var cmd tea.Cmd
	n.answer, cmd = n.answer.Update(msg)
	return nextKey{cmd: cmd}
}

// problemText is what the human reads: what diatom was doing, and the tail of
// what it printed.
func (n *Next) problemText(it item) string {
	p := it.problem
	var b strings.Builder
	fmt.Fprintf(&b, "%s while doing %s.\n", p.What, p.Op)
	if p.Count > 1 {
		fmt.Fprintf(&b, "\nIt has failed %d times, the last at %s.\n", p.Count, stamp(p.Last))
	}
	out := strings.TrimSpace(p.Text)
	if out == "" {
		return b.String()
	}
	lines := strings.Split(out, "\n")
	if len(lines) > problemTail {
		fmt.Fprintf(&b, "\nThe last %d of %d lines:\n", problemTail, len(lines))
		lines = lines[len(lines)-problemTail:]
	} else {
		b.WriteString("\nWhat it printed:\n")
	}
	b.WriteString("\n" + strings.Join(lines, "\n") + "\n")
	return b.String()
}
