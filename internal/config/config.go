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
	"encoding/json"
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

// MCPFileName is Claude Code's own project file, which diatom reads as a
// catalog so a repo that already declares its servers declares them nowhere
// else (ADR 0013).
const MCPFileName = ".mcp.json"

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
	// Fix formats and regenerates what the gate checks, such as
	// `mage ci:fix`, run with `sh -c` in the worktree before the gate when a
	// session ends, so an agent never runs it. Empty runs none. Unset, it
	// is the Fixes entry for the kind of project the repo is.
	Fix string `toml:"fix"`
	// Fixes maps a kind of project to the fix of a repo of that kind that
	// sets no fix of its own.
	Fixes map[string]string `toml:"fixes"`
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
	// ChainContext is the most context, in tokens, a session may have and
	// still go on to its next task: past it, marking a task done hands the
	// rest to fresh sessions, as each turn of a long session costs more than
	// a new one reading its base again (ADR 0004). 0 never hands them on.
	ChainContext int `toml:"chainContext"`
	// Budget caps what the repo's agent sessions cost.
	Budget Budget `toml:"budget"`
	// Editor is the command the reviewer's o opens a hunk's file in, at its
	// line, from the worktree of the workstream that made it. It takes the
	// terminal until it exits. VS Code and its forks are given the line their
	// own way, so "code" works as it stands. Empty turns o off.
	Editor string `toml:"editor"`
	// Land is how a goal ready to finish lands: LandMerge merges it into its
	// base branch, LandPRs opens its stacked pull requests. Empty offers both.
	Land string `toml:"land"`
	// Tickets names the repo's ticket tracker, such as "linear". Set, every
	// goal must carry a ticket, which names the scope of its pull request's
	// title; empty asks for none.
	Tickets string `toml:"tickets"`
	// CommitCheck lints a commit message: it is run with `sh -c` and the path
	// of a file holding the message appended, such as
	// `project-standards commit-msg`. The default runs the repo's own
	// commit-msg hook, which diatom's commits otherwise skip, and passes
	// where there is none. Empty checks only the Conventional Commits
	// header.
	CommitCheck string `toml:"commitCheck"`
	// ADR says where a goal's ADRs go and how they're written (ADR 0010).
	ADR ADR `toml:"adr"`
	// Instructions are files appended to every agent's system prompt. A
	// missing file is skipped. The AGENTS.md of every directory from the home
	// directory down to the repo's own are always added after them. Agents
	// get no other context unless it is configured here, in Skills, in a
	// profile's skills or in MCPServers (ADR 0006).
	Instructions []string `toml:"instructions"`
	// Skills are the skills every agent session may load, on top of its
	// profile's own: a bare name is a directory in ~/.claude/skills, anything
	// else a path to a skill directory.
	Skills []string `toml:"skills"`
	// MCPServers are the MCP servers agents may ask for, in Claude Code's
	// mcpServers format with an optional `purpose` added (ADR 0013). The
	// repo's own .mcp.json is read into this, so a repo that already declares
	// its servers needs nothing here but a purpose for the ones whose name
	// does not say what they are. None of the user's own servers are loaded,
	// and a session gets only the servers its tasks declare or it asks for.
	MCPServers map[string]any `toml:"mcpServers"`
	// MaxConnectors caps the MCP servers one session may carry, so a batch's
	// union stays small (ADR 0013). 0 does not cap them.
	MaxConnectors int `toml:"maxConnectors"`
}

// PurposeKey is the catalog entry diatom adds to Claude Code's mcpServers
// format, and strips again before passing the server on.
const PurposeKey = "purpose"

// Connector is one MCP server as the catalog describes it to an agent.
type Connector struct {
	Name    string
	Purpose string
}

// Connectors is the repo's catalog, by name: what every session is told
// exists, without any of the servers' tool schemas (ADR 0013).
func (c *Repo) Connectors() []Connector {
	cat := make([]Connector, 0, len(c.MCPServers))
	for name, v := range c.MCPServers {
		m, _ := v.(map[string]any)
		purpose, _ := m[PurposeKey].(string)
		cat = append(cat, Connector{Name: name, Purpose: purpose})
	}
	slices.SortFunc(cat, func(a, b Connector) int { return cmp.Compare(a.Name, b.Name) })
	return cat
}

// MCPConfig is the named servers in Claude Code's own format, with the
// catalog's purpose stripped. Unknown names are an error, so a stale task or
// a made-up request fails loudly instead of running without its connector.
func (c *Repo) MCPConfig(names []string) (map[string]any, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(names))
	for _, name := range names {
		v, ok := c.MCPServers[name]
		if !ok {
			return nil, fmt.Errorf("no MCP server %q in the config; it has %s",
				name, strings.Join(connectorNames(c.Connectors()), ", "))
		}
		m, ok := v.(map[string]any)
		if !ok {
			out[name] = v
			continue
		}
		server := make(map[string]any, len(m))
		for k, mv := range m {
			if k != PurposeKey {
				server[k] = mv
			}
		}
		out[name] = server
	}
	return out, nil
}

func connectorNames(cat []Connector) []string {
	names := make([]string, len(cat))
	for i, c := range cat {
		names[i] = c.Name
	}
	return names
}

// The ways a repo's goals land.
const (
	LandMerge = "merge"
	LandPRs   = "prs"
)

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

// Paths say where diatom's one config file is, and which repo is being
// configured. The zero value reads them from the environment.
type Paths struct {
	// Home is the user's home directory, which a leading ~ expands to.
	Home string
	// XDG is the diatom directory under XDG_CONFIG_HOME.
	XDG string
	// Key names the repo being configured, as state.Key makes it:
	// github.com/goodship-io/nextjs. The config file's [repos."<prefix>"]
	// blocks that cover it are read over its top level (ADR 0013). Empty
	// reads no block.
	Key string
}

// For is the paths with the repo key set, for loading one repo's config.
func (p Paths) For(key string) Paths { p.Key = key; return p }

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

// StateDir is where diatom keeps what it must remember across repos and
// restarts, such as the window's layout and the ledger of landed goals. It
// is the one root every repo's queue sits under too (ADR 0013):
// $XDG_DATA_HOME/diatom, or ~/.local/share/diatom.
func (p Paths) StateDir() string {
	if dir := os.Getenv("DIATOM_STATE"); dir != "" {
		return dir
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		dir = filepath.Join(p.Home, ".local", "share")
	}
	return filepath.Join(dir, "diatom")
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
		if c.Gate, err = kindCommand(root, "gates", c.Gates); err != nil {
			return nil, err
		}
	}
	if c.Fix == "" && root != "" {
		if c.Fix, err = kindCommand(root, "fixes", c.Fixes); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// SessionGate is what a session's work must pass before it ends: the fix,
// when there is one, then the gate, whose result decides.
func (c *Config) SessionGate() string {
	if strings.TrimSpace(c.Fix) == "" {
		return c.Gate
	}
	return c.Fix + "; " + c.Gate
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

// kindCommand is the command table gives the repo at root, such as its gate
// from gates, or "" when it gives none. A repo of two kinds that both have
// one must set its own.
func kindCommand(root, table string, gates map[string]string) (string, error) {
	for name := range gates {
		if !slices.ContainsFunc(kinds, func(k kind) bool { return k.name == name }) {
			return "", fmt.Errorf("config: %s.%s: no such kind of project; the kinds are %s",
				table, name, kindNames())
		}
	}
	var gate, from string
	for _, k := range Kinds(root) {
		g := strings.TrimSpace(gates[k])
		switch {
		case g == "":
		case gate != "" && g != gate:
			return "", fmt.Errorf("config: %s is both a %s and a %s project, whose %s differ: "+
				"set its own in the config's block for it", root, from, k, table)
		default:
			gate, from = g, k
		}
	}
	return gate, nil
}

func kindNames() string {
	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, k.name)
	}
	return strings.Join(names, ", ")
}

// walk returns the config layers for root, furthest (the defaults) first.
//
// There is one config file (ADR 0013): the defaults, then the file's top
// level, then every [repos."<prefix>"] block whose prefix the repo's key
// starts with, shortest prefix first, so a block for one repo refines the
// block for its organisation. The repo's own .mcp.json is the one file
// diatom still reads out of a repository, and it is nearest of all.
func walk(root string, paths Paths) ([]map[string]any, error) {
	base, err := parse(defaults, "defaults.toml")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(paths.XDG, FileName)
	file, err := read(path)
	if err != nil {
		return nil, err
	}
	blocks, err := overrides(file, path, paths.Key)
	if err != nil {
		return nil, err
	}
	servers, err := readMCP(filepath.Join(root, MCPFileName))
	if err != nil {
		return nil, err
	}
	delete(file, reposKey)
	layers := append([]map[string]any{base, file}, blocks...)
	return append(layers, servers), nil
}

// reposKey holds the per-repo overrides in the config file.
const reposKey = "repos"

// overrides are the [repos."<prefix>"] blocks that cover key, shortest
// prefix first. A block that sets a key only the file's top level may set is
// an error: a profile is the human's, not the repository's.
func overrides(file map[string]any, path, key string) ([]map[string]any, error) {
	repos, ok := file[reposKey].(map[string]any)
	if !ok {
		return nil, nil
	}
	var prefixes []string
	for prefix := range repos {
		if key != "" && covers(prefix, key) {
			prefixes = append(prefixes, prefix)
		}
	}
	slices.SortFunc(prefixes, func(a, b string) int { return len(a) - len(b) })
	var out []map[string]any
	for _, prefix := range prefixes {
		block, ok := repos[prefix].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: [%s.%q] is not a table", path, reposKey, prefix)
		}
		for _, k := range homeOnly {
			if _, set := block[k]; set {
				return nil, fmt.Errorf("%s: [%s.%q] sets %s, which is yours and not a repo's: "+
					"move it to the top of the file", path, reposKey, prefix, k)
			}
		}
		out = append(out, block)
	}
	return out, nil
}

// covers reports whether prefix names key: the whole key, or a run of its
// path elements from the start.
func covers(prefix, key string) bool {
	prefix = strings.Trim(prefix, "/")
	return key == prefix || strings.HasPrefix(key, prefix+"/")
}

// readMCP parses Claude Code's .mcp.json at path into a config layer. A
// missing file is an empty layer.
func readMCP(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(f.MCPServers) == 0 {
		return map[string]any{}, nil
	}
	return map[string]any{"mcpServers": f.MCPServers}, nil
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
	if c.Land != "" && c.Land != LandMerge && c.Land != LandPRs {
		return nil, fmt.Errorf("config: land is %q: it is %q, %q, or unset to offer both",
			c.Land, LandMerge, LandPRs)
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
