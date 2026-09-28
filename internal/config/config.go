// Package config loads diatom's configuration (ADR 0007). A repo's settings are
// merged from `.diatom/config.toml` in the repository root and in each of its
// parent directories, then the XDG file `~/.config/diatom/config.toml`, where
// the search stops. The closest file wins. Home-only settings (the profiles,
// and updating diatom itself) are read from the XDG file alone, and setting
// one anywhere else is an error.
package config

import (
	"bytes"
	"cmp"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DirName is the directory that holds diatom's state and config in a repo, and
// its config in any directory above one (ADR 0002).
const DirName = ".diatom"

// FileName is the config file inside DirName, and inside the XDG directory.
const FileName = "config.toml"

// oldFileName is the YAML config diatom read before, which is now an error
// so a setting in one is never silently ignored.
const oldFileName = "config.yaml"

// defaults is the lowest layer of every merge, below the XDG file.
//
//go:embed defaults.toml
var defaults []byte

// homeOnly are the top-level keys only the XDG file may set.
var homeOnly = []string{"profiles", "autoUpdate"}

// Repo is the configuration of one repository, merged by the walk-up.
type Repo struct {
	// Gate is the check command every commit must pass, run with `sh -c` in the
	// worktree (ADR 0005). It is never skipped, so an empty gate is an error.
	// Unset, it is the Gates entry for the kind of project the repo is.
	Gate string `toml:"gate"`
	// Gates maps a kind of project, such as go or node, to the gate of a
	// repo of that kind that sets no gate of its own.
	Gates map[string]string `toml:"gates"`
	// GateAttempts is how many times the Stop hook sends a failing gate back
	// to the agent before the task is retried with more effort.
	GateAttempts int `toml:"gateAttempts"`
	// GateTimeout is how long the gate may take. A gate run that takes this
	// long is stuck: it is stopped and fails.
	GateTimeout time.Duration `toml:"gateTimeout"`
	// CommandTimeout is how long any command an agent runs may take. It is
	// stopped after that, so agents run narrow checks and leave the whole
	// gate to diatom.
	CommandTimeout time.Duration `toml:"commandTimeout"`
	// AutoApprove are the files whose hunks are approved without review,
	// such as *_test.go. A pattern without a slash matches a file's name in
	// any directory; one with a slash matches its path from the repo's root,
	// and one ending in /** everything below a directory.
	AutoApprove []string `toml:"autoApprove"`
	// MaxSessions caps the agent sessions running in this repo at once.
	MaxSessions int `toml:"maxSessions"`
	// MaxBatch caps the tasks one session takes (ADR 0004).
	MaxBatch int `toml:"maxBatch"`
	// Budget caps what the repo's agent sessions cost.
	Budget Budget `toml:"budget"`
	// CommitCheck lints a commit message: it is run with `sh -c` and the path
	// of a file holding the message appended, such as
	// `project-standards commit-msg`. Empty checks only the Conventional
	// Commits header.
	CommitCheck string `toml:"commitCheck"`
	// ADR says where a goal's ADRs go and how they're written (ADR 0010).
	ADR ADR `toml:"adr"`
	// Instructions are files appended to every agent's system prompt, such
	// as ~/AGENTS.md. A missing file is skipped. The repo's own root AGENTS.md
	// is always added after them. Agents get no other context unless it is
	// configured here, in a profile's skills or in MCPServers (ADR 0006).
	Instructions []string `toml:"instructions"`
	// MCPServers are the MCP servers agents may use, in Claude Code's
	// mcpServers format. None of the user's own servers are loaded.
	MCPServers map[string]any `toml:"mcpServers"`
}

// Budget caps what a repo's agent sessions cost, in US dollars: today, over
// the last 7 days, and over the last 30. Once one is spent, no new session
// starts until the spending falls back under it; sessions already running
// carry on. Zero caps nothing.
type Budget struct {
	Day   float64 `toml:"day"`
	Week  float64 `toml:"week"`
	Month float64 `toml:"month"`
}

// ADR configures where ADRs go and what format they use.
type ADR struct {
	// Dir is the repo's ADR directory, relative to its root. Empty keeps ADRs
	// in the goal's directory.
	Dir string `toml:"dir"`
	// Format is free text given to the planning profile about how to write an
	// ADR, such as the name of a skill to follow.
	Format string `toml:"format"`
}

// Home is the configuration only the XDG file sets.
type Home struct {
	// Profiles maps each profile name to the agent that runs it (ADR 0006).
	Profiles map[string]Profile `toml:"profiles"`
	// AutoUpdate installs each new release of diatom as it comes out and
	// restarts the scheduler on it, resuming its sessions. A diatom built
	// from a checkout never updates itself.
	AutoUpdate bool `toml:"autoUpdate"`
}

// Profile is the kind of agent a piece of work needs.
type Profile struct {
	// Model is a model alias or full name, such as opus or claude-sonnet-5.
	Model string `toml:"model"`
	// Effort is the effort level, such as medium; empty uses the model's.
	Effort string `toml:"effort"`
	// Tools are the tools the agent may use, in Claude Code's permission
	// syntax (`Bash`, `Edit`, `Bash(go test:*)`). Empty allows none.
	Tools []string `toml:"tools"`
	// MaxTurns ends a session after this many agent turns.
	MaxTurns int `toml:"maxTurns"`
	// Skills are the skills the agent may load. A bare name is a directory
	// in ~/.claude/skills; anything else is a path to a skill directory.
	Skills []string `toml:"skills"`
}

// Config is a repo's merged configuration together with the home settings.
type Config struct {
	Repo
	Home
}

// Profile returns the named profile, or an error naming the missing one.
func (c *Config) Profile(name string) (Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("no profile %q in the config", name)
	}
	return p, nil
}

// Paths are the directories the walk-up uses. The zero value reads them from
// the environment.
type Paths struct {
	// Home is the directory the walk-up stops at, after reading it.
	Home string
	// XDG is the diatom directory under XDG_CONFIG_HOME.
	XDG string
}

// DefaultPaths returns the user's home and XDG config directories.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return Paths{Home: home, XDG: filepath.Join(xdg, "diatom")}, nil
}

// Expand resolves a leading ~ in path against the home directory.
func (p Paths) Expand(path string) string {
	if path == "~" {
		return p.Home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(p.Home, rest)
	}
	return path
}

// SkillDir resolves a profile's skill: a bare name is a directory in
// ~/.claude/skills, anything else a path.
func (p Paths) SkillDir(skill string) string {
	if !strings.ContainsRune(skill, '/') {
		return filepath.Join(p.Home, ".claude", "skills", skill)
	}
	return p.Expand(skill)
}

// Load merges the configuration for the repository rooted at root.
func Load(root string, paths Paths) (*Config, error) {
	layers, err := walk(root, paths)
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for _, l := range layers {
		merge(merged, l)
	}
	c, err := decode(merged)
	if err != nil {
		return nil, err
	}
	for _, p := range c.AutoApprove {
		if _, err := path.Match(strings.TrimSuffix(p, "/**"), ""); err != nil {
			return nil, fmt.Errorf("config: autoApprove %q: %w", p, err)
		}
	}
	if c.Gate == "" && root != "" {
		if c.Gate, err = kindGate(root, c.Gates); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// kind is a kind of project Gates may name, with the files at a repo's root
// that make it one.
type kind struct {
	name  string
	files []string
}

var kinds = []kind{
	{"go", []string{"go.mod"}},
	{"node", []string{"package.json"}},
	{"deno", []string{"deno.json", "deno.jsonc"}},
	{"rust", []string{"Cargo.toml"}},
	{"python", []string{"pyproject.toml"}},
}

// Kinds lists the kinds of project the repo at root is, such as go.
func Kinds(root string) []string {
	var found []string
	for _, k := range kinds {
		for _, f := range k.files {
			if _, err := os.Stat(filepath.Join(root, f)); err == nil {
				found = append(found, k.name)
				break
			}
		}
	}
	return found
}

// kindGate is the gate Gates gives the repo at root, or "" when it gives
// none. A repo of two kinds that both have one must set its own gate.
func kindGate(root string, gates map[string]string) (string, error) {
	for name := range gates {
		if !slices.ContainsFunc(kinds, func(k kind) bool { return k.name == name }) {
			return "", fmt.Errorf("config: gates.%s: no such kind of project; the kinds are %s",
				name, kindNames())
		}
	}
	var gate, from string
	for _, k := range Kinds(root) {
		g := strings.TrimSpace(gates[k])
		switch {
		case g == "":
		case gate != "" && g != gate:
			return "", fmt.Errorf("config: %s is both a %s and a %s project, whose gates differ: "+
				"set gate in %s", root, from, k, filepath.Join(root, DirName, FileName))
		default:
			gate, from = g, k
		}
	}
	return gate, nil
}

// SetGate saves gate as the gate of the repo at root, in its own config
// file.
func SetGate(root, gate string) error {
	path := filepath.Join(root, DirName, FileName)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]string{"gate": gate}); err != nil {
		return err
	}
	// A top-level key has to come before any table, so it goes first.
	body := append(b.Bytes(), old...)
	if _, err := parse(body, path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func kindNames() string {
	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, k.name)
	}
	return strings.Join(names, ", ")
}

// LoadHome reads the home settings alone, for the scheduler, which runs
// outside any one repo.
func LoadHome(paths Paths) (*Config, error) {
	return Load("", paths)
}

// walk returns the config layers for root, furthest (the defaults) first.
func walk(root string, paths Paths) ([]map[string]any, error) {
	base, err := parse(defaults, "defaults.toml")
	if err != nil {
		return nil, err
	}
	xdg, err := read(filepath.Join(paths.XDG, FileName))
	if err != nil {
		return nil, err
	}
	var near []map[string]any // closest first
	for dir := root; dir != ""; {
		path := filepath.Join(dir, DirName, FileName)
		layer, err := read(path)
		if err != nil {
			return nil, err
		}
		for _, k := range homeOnly {
			if _, ok := layer[k]; ok {
				return nil, fmt.Errorf("%s: %s is only read from %s", path, k,
					filepath.Join(paths.XDG, FileName))
			}
		}
		near = append(near, layer)
		parent := filepath.Dir(dir)
		if dir == paths.Home || parent == dir {
			break
		}
		dir = parent
	}
	slices.Reverse(near)
	return append([]map[string]any{base, xdg}, near...), nil
}

// read parses the TOML file at path. A missing file is an empty layer, and
// a YAML one beside it an error.
func read(path string) (map[string]any, error) {
	old := filepath.Join(filepath.Dir(path), oldFileName)
	if _, err := os.Stat(old); err == nil {
		return nil, fmt.Errorf("%s: diatom's config is TOML now: move it to %s", old, path)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return parse(b, path)
}

func parse(b []byte, name string) (map[string]any, error) {
	m := map[string]any{}
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

// merge folds src into dst. Maps merge key by key; anything else in src,
// lists included, replaces what dst had.
func merge(dst, src map[string]any) {
	for k, v := range src {
		sm, sok := v.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		if sok && dok {
			merge(dm, sm)
			continue
		}
		dst[k] = v
	}
}

// decode turns the merged layers into a Config, rejecting unknown keys.
func decode(m map[string]any) (*Config, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(m); err != nil {
		return nil, err
	}
	var c Config
	md, err := toml.Decode(b.String(), &c)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for _, key := range md.Undecoded() {
		// MCP servers are in Claude Code's format, which diatom passes on
		// as it is.
		if key[0] != "mcpServers" {
			return nil, fmt.Errorf("config: unknown setting %s: a typo, or a setting "+
				"newer than this diatom, which updating it would fix", key)
		}
	}
	return &c, nil
}

// efforts are the effort levels in order. A retry goes one step up but never
// past retryCeiling: it should think a little more, not become a different,
// far costlier agent.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

const retryCeiling = "xhigh"

// RetryEffort is the effort a failed task is retried at: one level above
// effort, capped at xhigh (ADR 0005). An unset effort counts as medium.
func RetryEffort(effort string) string {
	if effort == "" {
		effort = "medium"
	}
	i := slices.Index(efforts, effort)
	ceiling := slices.Index(efforts, retryCeiling)
	if i < 0 || i >= ceiling {
		return effort
	}
	return efforts[i+1]
}

// tiers orders the model families by capability, least first.
var tiers = []string{"haiku", "sonnet", "opus"}

// tier is a model's place in tiers, or -1 for one of no family it knows.
func tier(model string) int {
	return slices.IndexFunc(tiers, func(f string) bool { return strings.Contains(model, f) })
}

// Covers reports whether profile a can do profile b's work: the same one, or
// one on a model at least as capable, thinking at least as hard, with every
// tool and skill b has. A session that chains a task needing b onto work
// needing a stays on a (ADR 0004).
func (c *Config) Covers(a, b string) bool {
	if a == b {
		return true
	}
	pa, okA := c.Profiles[a]
	pb, okB := c.Profiles[b]
	if !okA || !okB {
		return false
	}
	ta, tb := tier(pa.Model), tier(pb.Model)
	if (ta < 0 || tb < 0) && pa.Model != pb.Model || ta < tb {
		return false
	}
	level := func(e string) int { return slices.Index(efforts, cmp.Or(e, "medium")) }
	return level(pa.Effort) >= level(pb.Effort) &&
		!slices.ContainsFunc(
			pb.Tools,
			func(t string) bool { return !slices.Contains(pa.Tools, t) },
		) &&
		!slices.ContainsFunc(
			pb.Skills,
			func(s string) bool { return !slices.Contains(pa.Skills, s) },
		)
}
