package hook

import (
	"slices"
	"strings"
)

// ghReadOnly are the gh subcommands, as `<command> <subcommand>`, that only
// read. Anything else is blocked: diatom opens and updates pull requests
// itself, and nothing an agent runs may merge one (ADR 0014).
var ghReadOnly = []string{
	"pr view", "pr list", "pr diff", "pr checks", "pr status",
	"run view", "run list", "run watch",
	"workflow view", "workflow list",
	"issue view", "issue list", "issue status",
	"release view", "release list",
	"repo view", "repo list",
	"label list",
	"cache list",
	"auth status",
	"search prs", "search issues", "search repos", "search code", "search commits",
	"browse --no-browser",
}

// ghWriteFields are the gh api options that make a request a write, either by
// setting a method or by sending a body, which gh sends as a POST.
var ghWriteFields = []string{
	"-X", "--method", "-f", "--raw-field", "-F", "--field", "--input",
}

// ghCommandReadOnly reports whether gh with these arguments only reads.
func ghCommandReadOnly(args []string) bool {
	p := positional(args)
	if len(p) == 0 {
		return true // `gh` alone prints help
	}
	if p[0] == "api" {
		return apiReadOnly(args)
	}
	if len(p) < 2 {
		// A bare command group prints its help; `gh pr` lists nothing.
		return !slices.Contains([]string{"alias", "extension", "auth", "config"}, p[0])
	}
	return slices.Contains(ghReadOnly, p[0]+" "+p[1])
}

// apiReadOnly reports whether a gh api call only reads: no method other than
// GET, and no body, which gh would send as a POST.
func apiReadOnly(args []string) bool {
	for i, a := range args {
		name, value, attached := strings.Cut(a, "=")
		if !slices.Contains(ghWriteFields, name) {
			continue
		}
		if name != "-X" && name != "--method" {
			return false
		}
		if !attached && i+1 < len(args) {
			value = args[i+1]
		}
		if !strings.EqualFold(value, "GET") {
			return false
		}
	}
	return true
}
