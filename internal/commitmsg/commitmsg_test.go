package commitmsg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/runner"
)

func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		{"feat(engine): add ward\n", "feat(engine): add ward\n"},
		{
			"Here is the message:\n\n```\nfix: stop double damage\n\nBody line.\n```\n",
			"fix: stop double damage\n\nBody line.\n",
		},
		{"  refactor!: split cards  \n", "refactor!: split cards\n"},
	}
	for _, tt := range tests {
		got, err := Clean(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("Clean(%q) = %q, %v, want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "Added ward.", "feature: x", "feat:x"} {
		if _, err := Clean(bad); err == nil {
			t.Errorf("Clean(%q) accepted it", bad)
		}
	}
}

func TestPrompt(t *testing.T) {
	p := Prompt(
		Input{
			Stat:   " a.go | 2 +-",
			Diff:   strings.Repeat("x", maxDiff+10),
			Titles: []string{"Add ward"},
		},
	)
	for _, want := range []string{"- Add ward", " a.go | 2 +-", "[diff cut"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// scripted answers each Run with the next reply.
type scripted struct {
	replies []string
	prompts []string
}

func (s *scripted) Run(
	_ context.Context,
	spec runner.Spec,
	_ func(runner.Event),
) (runner.Result, error) {
	s.prompts = append(s.prompts, spec.Prompt)
	if len(s.replies) == 0 {
		return runner.Result{}, errors.New("no reply left")
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return runner.Result{Text: r}, nil
}

func TestGenerateRetriesOnce(t *testing.T) {
	ctx := context.Background()
	r := &scripted{replies: []string{"I changed stuff.", "feat: add ward\n"}}
	msg, err := Generator{Runner: r}.Generate(ctx, Input{Dir: t.TempDir()})
	if err != nil || msg != "feat: add ward\n" {
		t.Fatalf("Generate = %q, %v", msg, err)
	}
	if len(r.prompts) != 2 || !strings.Contains(r.prompts[1], "I changed stuff.") {
		t.Errorf("retry prompt = %q", r.prompts)
	}

	r = &scripted{replies: []string{"nope", "still nope"}}
	if _, err := (Generator{Runner: r}).Generate(ctx, Input{Dir: t.TempDir()}); err == nil {
		t.Error("Generate accepted two bad messages")
	}
	if _, err := (Generator{Runner: &scripted{}}).Generate(
		ctx,
		Input{Dir: t.TempDir()},
	); err == nil {
		t.Error("Generate hid a runner error")
	}
}

func TestGenerateRunsCheck(t *testing.T) {
	ctx := context.Background()
	// The check rejects subjects over 20 characters.
	check := `test "$(head -1 "$1" | wc -c)" -le 21 || { echo "subject too long"; exit 1; }; true`
	g := Generator{
		Runner: &scripted{replies: []string{"feat: a very long subject line\n", "feat: short\n"}},
		Check:  "sh -c '" + check + "' check",
	}
	msg, err := g.Generate(ctx, Input{Dir: t.TempDir()})
	if err != nil || msg != "feat: short\n" {
		t.Errorf("Generate with check = %q, %v", msg, err)
	}
}
