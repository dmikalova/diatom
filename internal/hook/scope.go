package hook

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// FileTools are the tools whose paths Scope checks.
var FileTools = []string{"Read", "Grep", "Glob", "Edit", "MultiEdit", "Write", "NotebookEdit"}

// Scope is the part of diatom's state a session's file tools may reach: its
// own worktree and the directories it is given. Other goals come from
// `diatom task goals`, not from their files. The zero Scope allows every
// path, as outside a session.
type Scope struct {
	// State is the repo's .diatom directory.
	State string
	// Allowed are the directories inside State the session may use.
	Allowed []string
}

// deny is why the tool is blocked, or "" when every path it names is in
// scope. Bash isn't checked: what a command reads can't be told reliably.
func (s Scope) deny(ev toolInput) string {
	if s.State == "" || !slices.Contains(FileTools, ev.ToolName) {
		return ""
	}
	for _, p := range toolPaths(ev) {
		if !within(p, s.State) ||
			slices.ContainsFunc(s.Allowed, func(dir string) bool { return within(p, dir) }) {
			continue
		}
		return fmt.Sprintf(
			"`%s` is diatom's own state, outside your worktree, so %s is blocked there. The repo's "+
				"other goals, with their plans, workstreams and tasks, come from `diatom task goals "+
				"[<name>]`; the code is in your worktree.",
			p,
			ev.ToolName,
		)
	}
	return ""
}

// toolPaths are the absolute paths a file tool names. A Glob's pattern
// counts up to its first wildcard.
func toolPaths(ev toolInput) []string {
	in := ev.ToolInput
	var out []string
	for _, p := range []string{in.FilePath, in.NotebookPath, in.Path} {
		if p != "" {
			out = append(out, abs(ev.Cwd, p))
		}
	}
	if ev.ToolName == "Glob" && in.Pattern != "" {
		base := ev.Cwd
		if in.Path != "" {
			base = abs(ev.Cwd, in.Path)
		}
		out = append(out, staticPrefix(abs(base, in.Pattern)))
	}
	return out
}

func abs(cwd, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// staticPrefix is the directory of pattern before any element with a
// wildcard.
func staticPrefix(pattern string) string {
	parts := strings.Split(pattern, string(filepath.Separator))
	for i, part := range parts {
		if strings.ContainsAny(part, "*?[{") {
			return filepath.Clean(strings.Join(parts[:i], string(filepath.Separator)) + "/")
		}
	}
	return pattern
}

// within reports whether p is dir or below it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
