// Package intake is free-form input from the human waiting to be sorted into
// goals and tasks (ADR 0009): notes typed into the intake pane, and comments on
// approved hunks. Each intake is one Markdown file. An intake aimed at a goal
// sits in the goal's intake/ directory; one with no goal yet, such as a new
// goal, sits in the repo's .diatom/intake/. Triage moves a sorted intake to
// done/ beside it.
package intake

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Intake is one piece of input.
type Intake struct {
	// Source is where it came from: pane or review.
	Source  string    `yaml:"source"`
	Created time.Time `yaml:"created"`
	// Goal is the goal the human was looking at, a hint to where the
	// intake belongs; empty is the repo.
	Goal string `yaml:"goal,omitempty"`
	// Context is what the human had open when they sent it, such as a
	// question and its first lines: a clue for triage, not a rule.
	Context string `yaml:"context,omitempty"`
	// Commit and Hunk name the approved hunk a review comment was on.
	Commit string `yaml:"commit,omitempty"`
	Hunk   string `yaml:"hunk,omitempty"`

	// Path is the file, set when read.
	Path string `yaml:"-"`
	// Text is the input itself.
	Text string `yaml:"-"`
}

// Dir returns the repo's intake, where everything the human sends waits for
// triage (ADR 0009).
func Dir(repo string) string {
	return filepath.Join(repo, ".diatom", "intake")
}

// Write saves an intake in dir under a name sorted by time.
func Write(dir string, in Intake) (string, error) {
	front, err := yaml.Marshal(in)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body := "---\n" + string(front) + "---\n\n" + strings.TrimSpace(in.Text) + "\n"
	stamp := in.Created.UTC().Format("20060102T150405.000000000Z")
	f, err := os.CreateTemp(dir, "."+stamp+".*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, werr := f.WriteString(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	// A link, unlike a rename, never replaces an intake sent at the same
	// moment: that one keeps its name, and this one takes the next.
	for n := 0; ; n++ {
		name := stamp + ".md"
		if n > 0 {
			name = fmt.Sprintf("%s-%d.md", stamp, n)
		}
		path := filepath.Join(dir, name)
		err := os.Link(f.Name(), path)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return path, err
	}
}

// Pending lists the intakes in dir not yet triaged, oldest first.
func Pending(dir string) ([]Intake, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Intake
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		in, err := Read(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	// Oldest first. Intake sent at the same moment is numbered in the order
	// it was sent, and the number makes the name longer.
	slices.SortFunc(out, func(a, b Intake) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		if c := len(a.Path) - len(b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out, nil
}

// Read reads one intake.
func Read(path string) (Intake, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Intake{}, err
	}
	front, body, ok := strings.Cut(strings.TrimPrefix(string(b), "---\n"), "\n---\n")
	if !bytes.HasPrefix(b, []byte("---\n")) || !ok {
		return Intake{}, fmt.Errorf("%s: no YAML frontmatter", path)
	}
	var in Intake
	if err := yaml.Unmarshal([]byte(front), &in); err != nil {
		return Intake{}, fmt.Errorf("%s: %w", path, err)
	}
	in.Path, in.Text = path, strings.TrimSpace(body)
	return in, nil
}

// Done moves a triaged intake to done/ beside it.
func Done(in Intake) error {
	dir := filepath.Join(filepath.Dir(in.Path), "done")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Rename(in.Path, filepath.Join(dir, filepath.Base(in.Path)))
}
