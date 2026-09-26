// Package config loads diatom's configuration (ADR 0007). A repo's settings are
// merged from `.diatom/config.yaml` in the repository root and in each of its
// parent directories, then the XDG file `~/.config/diatom/config.yaml`, where
// the search stops. The closest file wins. Home-only settings (the profiles,
// the machine-wide session cap and the search roots) are read from the XDG file
// alone, and setting one anywhere else is an error.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DirName is the directory that holds diatom's state and config in a repo, and
// its config in any directory above one (ADR 0002).
const DirName = ".diatom"

// FileName is the config file inside DirName, and inside the XDG directory.
const FileName = "config.yaml"

// defaults is the lowest layer of every merge, below the XDG file.
//
//go:embed defaults.yaml
var defaults []byte

// homeOnly are the top-level keys only the XDG file may set.
var homeOnly = []string{"profiles", "machineSessions", "searchRoots"}

// Repo is the configuration of one repository, merged by the walk-up.
type Repo struct {
	// Gate is the check command every commit must pass, run with `sh -c` in the
	// worktree (ADR 0005). It is never skipped, so an empty gate is an error.
	Gate string `yaml:"gate"`
	// GateAttempts is how many times the Stop hook sends a failing gate back
	// to the agent before the task is retried with more effort.
	GateAttempts int `yaml:"gateAttempts"`
	// MaxSessions caps the agent sessions running in this repo at once.
	MaxSessions int `yaml:"maxSessions"`
	// MaxBatch caps the tasks one session takes (ADR 0004).
	MaxBatch int `yaml:"maxBatch"`
	// CommitCheck lints a commit message: it is run with `sh -c` and the path
	// of a file holding the message appended, such as
	// `project-standards commit-msg`. Empty checks only the Conventional
	// Commits header.
	CommitCheck string `yaml:"commitCheck"`
	// ADR says where a goal's ADRs go and how they're written (ADR 0010).
	ADR ADR `yaml:"adr"`
	// Instructions are files appended to every agent's system prompt, such
	// as ~/AGENTS.md. A missing file is skipped. The repo's own root AGENTS.md
	// is always added after them. Agents get no other context unless it is
	// configured here, in a profile's skills or in MCPServers (ADR 0006).
	Instructions []string `yaml:"instructions"`
	// MCPServers are the MCP servers agents may use, in Claude Code's
	// mcpServers format. None of the user's own servers are loaded.
	MCPServers map[string]any `yaml:"mcpServers"`
}

// ADR configures where ADRs go and what format they use.
type ADR struct {
	// Dir is the repo's ADR directory, relative to its root. Empty keeps ADRs
	// in the goal's directory.
	Dir string `yaml:"dir"`
	// Format is free text given to the planning profile about how to write an
	// ADR, such as the name of a skill to follow.
	Format string `yaml:"format"`
}

// Home is the configuration only the XDG file sets.
type Home struct {
	// Profiles maps each profile name to the agent that runs it (ADR 0006).
	Profiles map[string]Profile `yaml:"profiles"`
	// MachineSessions caps the sessions running across every repo; 0 is no cap.
	MachineSessions int `yaml:"machineSessions"`
	// SearchRoots are the directories the status pane's repo picker searches.
	SearchRoots []string `yaml:"searchRoots"`
}

// Profile is the kind of agent a piece of work needs.
type Profile struct {
	// Model is a model alias or full name, such as opus or claude-sonnet-5.
	Model string `yaml:"model"`
	// Effort is the effort level, such as medium; empty uses the model's.
	Effort string `yaml:"effort"`
	// Tools are the tools the agent may use, in Claude Code's permission
	// syntax (`Bash`, `Edit`, `Bash(go test:*)`). Empty allows none.
	Tools []string `yaml:"tools"`
	// MaxTurns ends a session after this many agent turns.
	MaxTurns int `yaml:"maxTurns"`
	// Skills are the skills the agent may load. A bare name is a directory
	// in ~/.claude/skills; anything else is a path to a skill directory.
	Skills []string `yaml:"skills"`
}

// Config is a repo's merged configuration together with the home settings.
type Config struct {
	Repo `yaml:",inline"`
	Home `yaml:",inline"`
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
	return decode(merged)
}

// LoadHome reads the home settings alone, for the scheduler, which runs
// outside any one repo.
func LoadHome(paths Paths) (*Config, error) {
	return Load("", paths)
}

// walk returns the config layers for root, furthest (the defaults) first.
func walk(root string, paths Paths) ([]map[string]any, error) {
	base, err := parse(defaults, "defaults.yaml")
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

// read parses the YAML file at path. A missing file is an empty layer.
func read(path string) (map[string]any, error) {
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
	if err := yaml.Unmarshal(b, &m); err != nil {
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
	b, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
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
