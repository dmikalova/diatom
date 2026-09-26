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
	// Commit and Hunk name the approved hunk a review comment was on.
	Commit string `yaml:"commit,omitempty"`
	Hunk   string `yaml:"hunk,omitempty"`

	// Path is the file, set when read.
	Path string `yaml:"-"`
	// Text is the input itself.
	Text string `yaml:"-"`
}

// Dir returns where a goal's intake goes, or the repo's when goal is empty.
func Dir(repo, goal string) string {
	if goal == "" {
		return filepath.Join(repo, ".diatom", "intake")
	}
	return filepath.Join(repo, ".diatom", "goals", goal, "intake")
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
	name := in.Created.UTC().Format("20060102T150405.000000000Z") + ".md"
	path := filepath.Join(dir, name)
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name())
		return "", werr
	}
	return path, os.Rename(f.Name(), path)
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
	slices.SortFunc(out, func(a, b Intake) int { return strings.Compare(a.Path, b.Path) })
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
