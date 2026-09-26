package hook

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// readOnly are the git subcommands an agent may run. Anything else, aliases
// included, is blocked: an allowlist fails closed when git grows a new way to
// write.
var readOnly = []string{
	"blame", "cat-file", "check-attr", "check-ignore", "count-objects", "describe",
	"diff", "diff-files", "diff-index", "diff-tree", "for-each-ref", "grep", "help",
	"log", "ls-files", "ls-remote", "ls-tree", "merge-base", "name-rev", "range-diff",
	"rev-list", "rev-parse", "shortlog", "show", "show-branch", "show-ref", "status",
	"var", "verify-commit", "verify-tag", "version", "whatchanged",
}

// listing are subcommands that are read-only only in some forms, each with a
// check of its arguments.
var listing = map[string]func(args []string) bool{
	"branch":   branchReadOnly,
	"tag":      tagReadOnly,
	"stash":    subIn("list", "show"),
	"worktree": subIn("list"),
	"remote":   func(a []string) bool { return len(positional(a)) == 0 || subIn("get-url", "show")(a) },
	"config":   hasAny("--get", "--get-all", "--get-regexp", "--list", "-l", "--show-origin"),
	"reflog":   func(a []string) bool { return len(positional(a)) == 0 || subIn("show")(a) },
	"notes":    func(a []string) bool { return len(positional(a)) == 0 || subIn("list", "show")(a) },
	"symbolic-ref": func(a []string) bool {
		return len(positional(a)) <= 1 && !hasAny("-d", "--delete")(a)
	},
	// Restoring or patching files is editing them; the index is the
	// harness's.
	"restore": func(a []string) bool { return !hasAny("--staged", "-S", "-W")(a) },
	"apply":   func(a []string) bool { return !hasAny("--index", "--cached", "-3", "--3way")(a) },
}

// gitOptionsWithValue are git's global options that take the next word.
var gitOptionsWithValue = []string{
	"-C",
	"-c",
	"--git-dir",
	"--work-tree",
	"--namespace",
	"--config-env",
}

// prefixes are commands that run the command after them.
var prefixes = []string{
	"sudo",
	"command",
	"env",
	"exec",
	"nice",
	"nohup",
	"time",
	"xargs",
	"builtin",
}

// shells run the script given with -c.
var shells = []string{"sh", "bash", "zsh", "dash", "ksh", "fish"}

// substitution matches a command substitution inside a word, which a shell
// runs even inside double quotes.
var substitution = regexp.MustCompile("\\$\\(([^)]*)\\)|`([^`]*)`")

// BlockedGit returns the first git command in a shell command line that could
// change the repository, or "" when there is none. It splits the line on
// operators and follows the places a quoted string is run as a command:
// `sh -c` and the other shells, eval, command substitutions and find -exec.
// Anywhere else a quoted string is only text, so a task note that mentions
// `git commit` is not mistaken for one.
func BlockedGit(line string) string {
	for _, cmd := range split(line) {
		if b := blockedCommand(cmd); b != "" {
			return b
		}
		for _, w := range cmd {
			for _, m := range substitution.FindAllStringSubmatch(w, -1) {
				if b := BlockedGit(m[1] + m[2]); b != "" {
					return b
				}
			}
		}
	}
	return ""
}

// blockedCommand checks one simple command, following the ones it runs.
func blockedCommand(cmd []string) string {
	words := cmd
	for len(words) > 0 && (strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") ||
		slices.Contains(prefixes, words[0])) {
		words = words[1:]
	}
	if len(words) == 0 {
		return ""
	}
	name, args := filepath.Base(words[0]), words[1:]
	switch {
	case name == "git":
		if !gitReadOnly(args) {
			return strings.Join(cmd, " ")
		}
	case name == "eval":
		return BlockedGit(strings.Join(args, " "))
	case slices.Contains(shells, name):
		for i, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") &&
				strings.Contains(a, "c") &&
				i+1 < len(args) {
				return BlockedGit(args[i+1])
			}
		}
	case name == "find":
		for i, a := range args {
			if a == "-exec" || a == "-execdir" || a == "-ok" || a == "-okdir" {
				if b := blockedCommand(args[i+1:]); b != "" {
					return b
				}
			}
		}
	}
	return ""
}

// gitReadOnly reports whether git with these arguments only reads.
func gitReadOnly(args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		opt := args[0]
		args = args[1:]
		if slices.Contains(gitOptionsWithValue, opt) && len(args) > 0 {
			args = args[1:]
		}
		if opt == "--version" || opt == "--help" {
			return true
		}
	}
	if len(args) == 0 {
		return true
	}
	sub, rest := args[0], args[1:]
	if slices.Contains(readOnly, sub) {
		return true
	}
	if check, ok := listing[sub]; ok {
		return check(rest)
	}
	return false
}

func branchReadOnly(a []string) bool {
	if hasAny(
		"-d",
		"-D",
		"--delete",
		"-m",
		"-M",
		"--move",
		"-c",
		"-C",
		"--copy",
		"-f",
		"--force",
		"-u",
		"--set-upstream-to",
		"--unset-upstream",
		"--edit-description",
		"--track",
		"--no-track",
	)(
		a,
	) {
		return false
	}
	return len(positional(a)) == 0 ||
		hasAny("-l", "--list", "-a", "--all", "-r", "--remotes", "--contains", "--no-contains",
			"--merged", "--no-merged", "--points-at", "--show-current")(a)
}

func tagReadOnly(a []string) bool {
	if hasAny(
		"-d",
		"--delete",
		"-f",
		"--force",
		"-a",
		"--annotate",
		"-s",
		"--sign",
		"-m",
		"-F",
	)(
		a,
	) {
		return false
	}
	return len(positional(a)) == 0 ||
		hasAny(
			"-l",
			"--list",
			"--contains",
			"--no-contains",
			"--merged",
			"--no-merged",
			"--points-at",
			"-v",
			"--verify",
		)(
			a,
		)
}

// subIn reports whether the first positional argument is one of subs.
func subIn(subs ...string) func([]string) bool {
	return func(a []string) bool {
		p := positional(a)
		return len(p) > 0 && slices.Contains(subs, p[0])
	}
}

// hasAny reports whether any argument is one of flags, or one of them with
// an attached =value.
func hasAny(flags ...string) func([]string) bool {
	return func(a []string) bool {
		return slices.ContainsFunc(a, func(arg string) bool {
			name, _, _ := strings.Cut(arg, "=")
			return slices.Contains(flags, name)
		})
	}
}

func positional(a []string) []string {
	var p []string
	for _, arg := range a {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			p = append(p, arg)
		}
	}
	return p
}

// split breaks a command line into simple commands, each a list of words with
// quotes removed. It splits on newlines, `;`, `&`, `|`, parentheses, braces,
// `$(` and backticks, and honors single quotes, double quotes and
// backslashes.
func split(line string) [][]string {
	var (
		cmds  [][]string
		words []string
		word  strings.Builder
		inW   bool
		quote rune
	)
	endWord := func() {
		if inW {
			words = append(words, word.String())
			word.Reset()
			inW = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			switch {
			case r == quote:
				quote = 0
			case r == '\\' && quote == '"' && i+1 < len(rs):
				i++
				word.WriteRune(rs[i])
			default:
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inW = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			if rs[i] != '\n' {
				word.WriteRune(rs[i])
				inW = true
			}
		case strings.ContainsRune(";&|(){}`\n", r) || r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			endCmd()
		case r == ' ' || r == '\t':
			endWord()
		default:
			word.WriteRune(r)
			inW = true
		}
	}
	endCmd()
	return cmds
}
