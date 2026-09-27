package hook

import (
	"path/filepath"
	"slices"
	"strings"
)

// detachers start a command that outlives the one the agent ran.
var detachers = []string{"nohup", "setsid", "disown", "daemonize"}

// BlockedWait returns what in a shell command line waits out the command
// time limit, or "" when nothing does: a command sent to the background with
// `&` or a detacher such as nohup, or a sleep. A headless session can't be
// woken when a background command ends, and a sleep only spends the time
// limit, so an agent doing either is polling for something it should have
// run in the foreground.
func BlockedWait(line string) string {
	if backgrounds(line) {
		return "&"
	}
	for _, cmd := range split(line) {
		if b := waitCommand(cmd); b != "" {
			return b
		}
	}
	return ""
}

// waitCommand checks one simple command, following the script a shell runs
// with -c.
func waitCommand(cmd []string) string {
	words := cmd
	for len(words) > 0 && (strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") ||
		slices.Contains(prefixes, words[0])) {
		if slices.Contains(detachers, words[0]) {
			return strings.Join(cmd, " ")
		}
		words = words[1:]
	}
	if len(words) == 0 {
		return ""
	}
	name, args := filepath.Base(words[0]), words[1:]
	switch {
	case name == "sleep" || slices.Contains(detachers, name):
		return strings.Join(cmd, " ")
	case slices.Contains(shells, name):
		for i, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") &&
				strings.Contains(a, "c") && i+1 < len(args) {
				return BlockedWait(args[i+1])
			}
		}
	}
	return ""
}

// backgrounds reports whether line has a lone `&` outside quotes, which
// runs the command before it in the background. `&&`, and the `&` of a
// redirection such as `2>&1` or `&>`, don't.
func backgrounds(line string) bool {
	rs := []rune(line)
	var quote rune
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				i++
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '\\':
			i++
		case r == '&':
			switch {
			case i+1 < len(rs) && rs[i+1] == '&':
				i++
			case i+1 < len(rs) && rs[i+1] == '>', i > 0 && strings.ContainsRune("><|", rs[i-1]):
			default:
				return true
			}
		}
	}
	return false
}
