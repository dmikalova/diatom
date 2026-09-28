// Package commitmsg writes a commit's message with a separate call on the
// cheapest profile (ADR 0005), following the dotfiles `coco` script. The agent
// is given the staged diff and the titles of the tasks the commit covers and
// must produce a Conventional Commits message. Any text before the first
// commit line is stripped, the message is checked with the repo's commitlint
// when there is one, and on failure it is retried once with the error.
package commitmsg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/runner"
)

// header matches a Conventional Commits subject line.
var header = regexp.MustCompile(
	`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]+\))?!?: \S`,
)

// maxDiff is how much of the diff the prompt carries; the stat always goes in
// whole, so a huge diff still gets an accurate subject.
const maxDiff = 60 << 10

// Generator writes commit messages.
type Generator struct {
	Runner  runner.Runner
	Profile config.Profile
	// Check lints a message file: `sh -c` runs it with the file's path
	// appended. Empty checks only the Conventional Commits header.
	Check string
}

// Input is what a commit is made of.
type Input struct {
	// Dir is the worktree, where the check runs.
	Dir    string
	Stat   string
	Diff   string
	Titles []string
}

// Generate returns a checked commit message for the staged change.
func (g Generator) Generate(
	ctx context.Context,
	in Input,
) (msg string, costUSD float64, err error) {
	prompt := Prompt(in)
	var lastErr error
	for range 2 {
		res, err := g.Runner.Run(
			ctx,
			runner.Spec{Dir: in.Dir, Prompt: prompt, Profile: g.Profile},
			func(runner.Event) {},
		)
		costUSD += res.Usage.CostUSD
		if err != nil {
			return "", costUSD, err
		}
		msg, err := Clean(res.Text)
		if err == nil {
			err = g.check(ctx, in.Dir, msg)
		}
		if err == nil {
			return msg, costUSD, nil
		}
		lastErr = err
		prompt += fmt.Sprintf(
			"\n\nYour last message was rejected:\n\n%s\n\nError:\n\n%s\n\nWrite a corrected message.",
			strings.TrimSpace(res.Text),
			err,
		)
	}
	return "", costUSD, fmt.Errorf("commit message: %w", lastErr)
}

// Prompt builds the instructions for one commit message.
func Prompt(in Input) string {
	diff := in.Diff
	if len(diff) > maxDiff {
		diff = diff[:maxDiff] + "\n[diff cut; the stat above covers every file]"
	}
	var b strings.Builder
	b.WriteString("Write a semantic commit message (Conventional Commits format) for this change. ")
	b.WriteString("Output ONLY the commit message, nothing else. ")
	b.WriteString("Use a short subject line and optionally a body separated by a blank line. ")
	fmt.Fprintf(&b, "Wrap the body at %d columns.\n\n", BodyWidth)
	if len(in.Titles) > 0 {
		b.WriteString("The change completes these tasks:\n\n")
		for _, t := range in.Titles {
			b.WriteString("- " + t + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Stat:\n\n" + in.Stat + "\n\nDiff:\n\n" + diff + "\n")
	return b.String()
}

// Clean drops anything before the first Conventional Commits line, and any
// code fence around the message.
func Clean(text string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if header.MatchString(strings.TrimSpace(l)) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", errors.New("no Conventional Commits subject line in the output")
	}
	lines = lines[start:]
	lines[0] = strings.TrimSpace(lines[0])
	if i := indexFence(lines); i >= 0 {
		lines = lines[:i]
	}
	return Wrap(strings.TrimSpace(strings.Join(lines, "\n"))) + "\n", nil
}

// BodyWidth is the column a commit body wraps at: git's convention, well
// inside any commitlint's line limit.
const BodyWidth = 72

// listItem matches the start of a list item in a body: a bullet or a number.
var listItem = regexp.MustCompile(`^(\s*(?:[-*+]|\d+[.)])\s+)`)

// trailer matches a git trailer line, such as Co-authored-by: or
// BREAKING CHANGE:, which must stay on a line of its own.
var trailer = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z-]*|BREAKING CHANGE): \S`)

// Wrap wraps a message's body at BodyWidth, whatever the model wrote: each
// paragraph and each list item is filled to the width, a list item's later
// lines hanging under its text. The subject, indented code and trailers are
// left as they are.
func Wrap(msg string) string {
	subject, body, ok := strings.Cut(msg, "\n")
	if !ok {
		return msg
	}
	blocks := strings.Split(strings.Trim(body, "\n"), "\n\n")
	for i, block := range blocks {
		blocks[i] = wrapBlock(block)
	}
	return subject + "\n\n" + strings.Join(blocks, "\n\n")
}

// wrapBlock fills one paragraph of a body, or each item of a list.
func wrapBlock(block string) string {
	lines := strings.Split(block, "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t") || trailer.MatchString(l) {
			return block
		}
	}
	var out []string
	var mark, text string
	flush := func() {
		if mark != "" || text != "" {
			out = append(out, fill(mark, text)...)
		}
		mark, text = "", ""
	}
	for _, l := range lines {
		if m := listItem.FindString(l); m != "" {
			flush()
			mark, text = m, strings.TrimSpace(l[len(m):])
			continue
		}
		text = strings.TrimSpace(text + " " + strings.TrimSpace(l))
	}
	flush()
	return strings.Join(out, "\n")
}

// fill wraps text at BodyWidth after mark, the later lines indented under
// the text. A word longer than the width, such as a URL, keeps a line of its
// own.
func fill(mark, text string) []string {
	indent := strings.Repeat(" ", len(mark))
	var lines []string
	line := mark
	for w := range strings.FieldsSeq(text) {
		switch {
		case len(line) == len(mark):
			line += w
		case len(line)+1+len(w) > BodyWidth:
			lines = append(lines, line)
			line = indent + w
		default:
			line += " " + w
		}
	}
	return append(lines, line)
}

func indexFence(lines []string) int {
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			return i
		}
	}
	return -1
}

// check runs the repo's commit linter on msg.
func (g Generator) check(ctx context.Context, dir, msg string) error {
	if g.Check == "" {
		return nil
	}
	f, err := os.CreateTemp("", "diatom-commit-msg-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(msg); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", g.Check+` "$0"`, f.Name())
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", g.Check, err, gate.Tail(string(out), 40))
	}
	return nil
}
