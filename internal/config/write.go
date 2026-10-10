package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is the one config file's path.
func File(paths Paths) string { return filepath.Join(paths.XDG, FileName) }

// Own is what the config file's block for key sets itself, as against what
// the repo takes from the level above it.
func Own(paths Paths, key string) (map[string]any, error) {
	if paths.XDG == "" {
		p, err := DefaultPaths()
		if err != nil {
			return nil, err
		}
		paths = p
	}
	file, err := read(File(paths))
	if err != nil {
		return nil, err
	}
	repos, _ := file[reposKey].(map[string]any)
	block, _ := repos[key].(map[string]any)
	return block, nil
}

// Value is what the config says key is, by the key's name in the file. It
// reports whether there is such a key at all.
func (c *Config) Value(key string) (any, bool) {
	for _, v := range []reflect.Value{reflect.ValueOf(c.Repo), reflect.ValueOf(c.Home)} {
		t := v.Type()
		for i := range t.NumField() {
			if t.Field(i).Tag.Get("toml") == key {
				return v.Field(i).Interface(), true
			}
		}
	}
	return nil, false
}

// Setting is one lever of the config a repo may set, as the window's repo
// page shows it: scalars only, because a map or a list reads far better in
// the file than in a form (ADR 0013).
type Setting struct {
	Key  string
	What string
	Kind SettingKind
}

// SettingKind is what a setting holds, which decides how the page edits it.
type SettingKind int

// The kinds of setting.
const (
	Text SettingKind = iota
	Number
	Duration
	Flag
)

// Settings are the repo levers the window writes, in the order the page
// lists them. Everything else in the config is a map or a list, and is the
// file's to edit.
var Settings = []Setting{
	{"gate", "The command every session's work must pass", Text},
	{"fix", "The command run before the gate, to fix what it can", Text},
	{"gateAttempts", "How many failed gates go back to the agent", Number},
	{"gateTimeout", "How long the gate may run before it counts as stuck", Duration},
	{"commandTimeout", "How long any other command may run", Duration},
	{"maxSessions", "How many sessions of this repo run at once", Number},
	{"maxBatch", "How many tasks one session may take", Number},
	{
		"chainContext",
		"The context, in tokens, past which a task's siblings go to fresh sessions",
		Number,
	},
	{"budget", "What this repo may spend a day, in US dollars", Text},
	{"editor", "The editor the window opens", Text},
	{"land", "How goals land: merge, prs, or empty for either", Text},
	{"tickets", "Where the tickets of this repo's goals live", Text},
	{"commitCheck", "The command that must pass before a commit", Text},
	{"adr", "Where the repo keeps its decision records", Text},
	{"instructions", "The file agents are told to read first", Text},
	{"maxConnectors", "How many MCP servers one session may take", Number},
	{"autoUpdate", "Whether diatom updates itself", Flag},
}

// Repo is a setting a repo may set. autoUpdate is the human's, so it is
// never written into an override block.
func (s Setting) Repo() bool { return !slices.Contains(homeOnly, s.Key) }

// Set writes values into the config file: into the [repos."<key>"] block
// when key names a repo, and at the top level when it is empty. Only the
// keys given are touched; everything else in the file, comments included, is
// left as it was.
//
// A value of nil deletes the key, which falls the repo back to the level
// above it.
func Set(paths Paths, key string, values map[string]any) error {
	path := filepath.Join(paths.XDG, FileName)
	file, err := read(path)
	if err != nil {
		return err
	}
	into := file
	if key != "" {
		repos, _ := file[reposKey].(map[string]any)
		if repos == nil {
			repos = map[string]any{}
			file[reposKey] = repos
		}
		block, _ := repos[key].(map[string]any)
		if block == nil {
			block = map[string]any{}
			repos[key] = block
		}
		into = block
	}
	for k, v := range values {
		for _, home := range homeOnly {
			if k == home && key != "" {
				return fmt.Errorf("%s is yours, not a repo's: it is set once, at the top of %s",
					k, path)
			}
		}
		if v == nil {
			delete(into, k)
			continue
		}
		into[k] = v
	}
	return writeFile(path, file)
}

// write puts the whole config back, annotated. Diatom owns this file: it
// explains every lever itself, so the comments in it are always the ones
// that match the diatom reading them (ADR 0013).
func writeFile(path string, file map[string]any) error {
	var b strings.Builder
	b.WriteString(header)
	top := map[string]any{}
	for k, v := range file {
		if k != reposKey {
			top[k] = v
		}
	}
	body, err := encode(top)
	if err != nil {
		return err
	}
	b.WriteString(body)
	repos, _ := file[reposKey].(map[string]any)
	for _, key := range slices.Sorted(keys(repos)) {
		block, ok := repos[key].(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n# %s\n[%s.%q]\n", aboutBlock(key), reposKey, key)
		body, err := encode(block)
		if err != nil {
			return err
		}
		b.WriteString(body)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// encode writes a flat table as TOML, each known key under the one line that
// says what it is for. Anything diatom does not know is kept, at the end,
// marked so the human can see it is doing nothing.
func encode(m map[string]any) (string, error) {
	var b strings.Builder
	left := map[string]any{}
	maps.Copy(left, m)
	for _, s := range Settings {
		v, ok := left[s.Key]
		if !ok {
			continue
		}
		delete(left, s.Key)
		line, err := pair(s.Key, v)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n# %s.\n%s", s.What, line)
	}
	var unknown []string
	for _, k := range slices.Sorted(keys(left)) {
		if _, ok := left[k].(map[string]any); ok {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) > 0 {
		b.WriteString("\n# Diatom does not know these keys, and does nothing with them.\n")
		for _, k := range unknown {
			line, err := pair(k, left[k])
			if err != nil {
				return "", err
			}
			b.WriteString(line)
			delete(left, k)
		}
	}
	// The tables go last: TOML reads everything after a table header as part
	// of it.
	for _, k := range slices.Sorted(keys(left)) {
		var t bytes.Buffer
		if err := toml.NewEncoder(&t).Encode(map[string]any{k: left[k]}); err != nil {
			return "", err
		}
		b.WriteString("\n" + t.String())
	}
	return b.String(), nil
}

// pair is one key and value as a TOML line.
func pair(k string, v any) (string, error) {
	var t bytes.Buffer
	if err := toml.NewEncoder(&t).Encode(map[string]any{k: v}); err != nil {
		return "", fmt.Errorf("writing %s: %w", k, err)
	}
	return t.String(), nil
}

func keys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// aboutBlock says what a block's prefix covers.
func aboutBlock(key string) string {
	if strings.Count(key, "/") >= 2 {
		return "The repo " + key + "."
	}
	return "Every repo under " + key + "."
}

// Ensure writes the config file the first time, with nothing set: the header
// says what the file is and the defaults say what the levers do.
func Ensure(paths Paths) error {
	path := filepath.Join(paths.XDG, FileName)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeFile(path, map[string]any{})
}

// header is what the top of the config file says. It is written afresh every
// time diatom saves the file.
const header = `# diatom's config. Diatom writes this file, so edit it when diatom is closed.
#
# The top of the file is what every repo gets. A [repos."<prefix>"] block
# below it is what the repos under that prefix get instead, and a longer
# prefix wins over a shorter one. The prefix names a repo the way its origin
# does, without a scheme or a .git:
#
#   [repos."github.com/goodship-io"]       every repo of the organisation
#   [repos."github.com/goodship-io/web"]   that one repo
#
# profiles and autoUpdate are yours, not a repo's: they are set here at the
# top and nowhere else. Every other lever can go in a block.
#
# A lever left out takes diatom's own default. The window's repo page writes
# the blocks for you.
`
