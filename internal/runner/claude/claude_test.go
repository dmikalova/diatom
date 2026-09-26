package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/runner"
)

// stream is trimmed from a real `claude -p --output-format stream-json` run.
const stream = `{"type":"system","subtype":"init","session_id":"abc"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"Looking at ward."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...\nmore","description":"x"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"engine/ward.go"}}]}}
not json at all
{"type":"result","subtype":"success","is_error":false,"session_id":"abc","num_turns":3,"result":"Done.","total_cost_usd":0.0142,"usage":{"input_tokens":9,"cache_creation_input_tokens":6552,"cache_read_input_tokens":100,"output_tokens":40}}
`

func TestParse(t *testing.T) {
	var events []runner.Event
	res, saw, err := Parse(
		strings.NewReader(stream),
		func(e runner.Event) { events = append(events, e) },
	)
	if err != nil || !saw {
		t.Fatalf("Parse = %v, saw result %v", err, saw)
	}
	want := runner.Result{
		SessionID: "abc",
		Outcome:   runner.Completed,
		Text:      "Done.",
		Turns:     3,
		Usage: runner.Usage{
			InputTokens:   9,
			OutputTokens:  40,
			CacheCreation: 6552,
			CacheRead:     100,
			CostUSD:       0.0142,
		},
	}
	if res != want {
		t.Errorf("result = %+v\nwant %+v", res, want)
	}
	wantEvents := []runner.Event{
		{Type: runner.EventText, Text: "Looking at ward."},
		{Type: runner.EventTool, Text: "Bash go test ./..."},
		{Type: runner.EventTool, Text: "Edit engine/ward.go"},
	}
	if !slices.Equal(events, wantEvents) {
		t.Errorf("events = %+v", events)
	}
}

func TestParseOutcomes(t *testing.T) {
	for line, want := range map[string]runner.Outcome{
		`{"type":"result","subtype":"error_max_turns","is_error":true}`:        runner.TurnLimit,
		`{"type":"result","subtype":"error_during_execution","is_error":true}`: runner.Failed,
		`{"type":"result","subtype":"success","is_error":true}`:                runner.Failed,
	} {
		res, _, _ := Parse(strings.NewReader(line), func(runner.Event) {})
		if res.Outcome != want {
			t.Errorf("%s: outcome %s, want %s", line, res.Outcome, want)
		}
	}
	if _, saw, _ := Parse(strings.NewReader(`{"type":"system"}`), func(runner.Event) {}); saw {
		t.Error("Parse saw a result in a stream without one")
	}
}

func TestArgs(t *testing.T) {
	args, err := Args(runner.Spec{
		Profile: config.Profile{
			Model:    "opus",
			Effort:   "high",
			MaxTurns: 50,
			Tools:    []string{"Read", "Bash(go test:*)", "Bash"},
		},
		Hooks: runner.Hooks{
			PreToolUse: "/bin/diatom hook pre-tool-use",
			Stop:       "/bin/diatom hook stop",
		},
		AddDirs:      []string{"/repo/.diatom/goals/g/tasks/active"},
		Instructions: "Be terse.",
		Skills:       []string{"/skills/grilling"},
		MCPServers:   map[string]any{"docs": map[string]any{"command": "docs-mcp"}},
	}, "/scratch")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{
		"-p --output-format stream-json --verbose",
		"--setting-sources project,local --strict-mcp-config",
		"--model opus", "--effort high", "--max-turns 50",
		"--tools Read,Bash ", "--allowedTools Read,Bash(go test:*),Bash",
		"--add-dir /repo/.diatom/goals/g/tasks/active",
		"--append-system-prompt-file /scratch/instructions.md",
		"--plugin-dir /scratch/plugin",
		`--mcp-config {"mcpServers":{"docs":{"command":"docs-mcp"}}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
	var settings struct {
		DisableBundledSkills bool                     `json:"disableBundledSkills"`
		Hooks                map[string][]hookMatcher `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &settings); err != nil {
		t.Fatalf("settings %q: %v", args[len(args)-1], err)
	}
	if !settings.DisableBundledSkills || settings.Hooks["PreToolUse"][0].Matcher != "Bash" ||
		settings.Hooks["Stop"][0].Hooks[0].Command != "/bin/diatom hook stop" {
		t.Errorf("settings = %+v", settings)
	}

	bare, _ := Args(runner.Spec{Profile: config.Profile{Model: "haiku"}}, "/scratch")
	joined := strings.Join(bare, " ")
	for _, unwanted := range []string{"--plugin-dir", "--append-system-prompt-file", "--mcp-config", "hooks"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("bare args = %q, want no %s", bare, unwanted)
		}
	}
	if !slices.Contains(bare, "--strict-mcp-config") ||
		bare[slices.Index(bare, "--tools")+1] != "" {
		t.Errorf("bare args = %q, want no tools and isolation kept", bare)
	}
}

func TestWriteScratch(t *testing.T) {
	skill := filepath.Join(t.TempDir(), "grilling")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skill, "SKILL.md"),
		[]byte("---\nname: grilling\n---\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := writeScratch(
		runner.Spec{Instructions: "Be terse.", Skills: []string{skill}},
		scratch,
	); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(scratch, "instructions.md")); string(b) != "Be terse." {
		t.Errorf("instructions.md = %q", b)
	}
	if _, err := os.Stat(
		filepath.Join(scratch, "plugin", "skills", "grilling", "SKILL.md"),
	); err != nil {
		t.Errorf("skill not in the plugin: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(scratch, "plugin", ".claude-plugin", "plugin.json"),
	); err != nil {
		t.Errorf("plugin manifest missing: %v", err)
	}
	if err := writeScratch(
		runner.Spec{Skills: []string{"/no/such/skill"}},
		t.TempDir(),
	); err == nil {
		t.Error("a missing skill was accepted")
	}
}

// fakeClaude writes a script that stands in for claude: it records its
// arguments, stdin and environment, then prints body.
func fakeClaude(t *testing.T, body string, exit int) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	out := filepath.Join(dir, "stream")
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"echo \"$@\" > " + filepath.Join(dir, "args") + "\n" +
		"cat > " + filepath.Join(dir, "stdin") + "\n" +
		"echo \"$DIATOM_SESSION\" > " + filepath.Join(dir, "env") + "\n" +
		"cat " + out + "\n" +
		"echo 'something broke' >&2\n" +
		"exit " + string(rune('0'+exit)) + "\n"
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestRun(t *testing.T) {
	bin, dir := fakeClaude(t, stream, 0)
	var n int
	res, err := Runner{Binary: bin}.Run(context.Background(), runner.Spec{
		Dir: t.TempDir(), Prompt: "Do the tasks.", Env: []string{"DIATOM_SESSION=/s/1"},
		Profile: config.Profile{Model: "sonnet"},
	}, func(runner.Event) { n++ })
	if err != nil || res.Outcome != runner.Completed || n != 3 {
		t.Fatalf("Run = %+v, %v, %d events", res, err, n)
	}
	for file, want := range map[string]string{"stdin": "Do the tasks.", "env": "/s/1", "args": "--model sonnet"} {
		b, _ := os.ReadFile(filepath.Join(dir, file))
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("%s = %q, want %q", file, b, want)
		}
	}
}

func TestRunErrors(t *testing.T) {
	ctx := context.Background()
	// A non-zero exit with a result is the result.
	bin, _ := fakeClaude(t, `{"type":"result","subtype":"error_max_turns","is_error":true}`+"\n", 1)
	res, err := Runner{Binary: bin}.Run(ctx, runner.Spec{Dir: t.TempDir()}, func(runner.Event) {})
	if err != nil || res.Outcome != runner.TurnLimit {
		t.Errorf("turn limit = %+v, %v", res, err)
	}
	// Without one, the failure carries stderr.
	bin, _ = fakeClaude(t, "", 1)
	if _, err := (Runner{Binary: bin}).Run(
		ctx,
		runner.Spec{Dir: t.TempDir()},
		func(runner.Event) {},
	); err == nil ||
		!strings.Contains(err.Error(), "something broke") {
		t.Errorf("crash error = %v", err)
	}
	bin, _ = fakeClaude(t, "", 0)
	if _, err := (Runner{Binary: bin}).Run(
		ctx,
		runner.Spec{Dir: t.TempDir()},
		func(runner.Event) {},
	); err == nil {
		t.Error("a clean exit without a result returned no error")
	}
	if _, err := (Runner{Binary: filepath.Join(t.TempDir(), "missing")}).Run(
		ctx,
		runner.Spec{},
		func(runner.Event) {},
	); err == nil {
		t.Error("a missing binary returned no error")
	}
}
