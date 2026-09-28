package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/runner"
)

// stream is trimmed from a real `claude -p --output-format stream-json` run.
const stream = `{"type":"system","subtype":"init","session_id":"abc"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"Looking at ward."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./...\nmore","description":"Run the tests"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"FAIL ward","is_error":true}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"engine/ward.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"edited"}]}]}}
not json at all
{"type":"result","subtype":"success","is_error":false,"session_id":"abc","num_turns":3,"result":"Done.","total_cost_usd":0.0142,"usage":{"input_tokens":9,"cache_creation_input_tokens":6552,"cache_read_input_tokens":100,"output_tokens":40}}
`

func TestParse(t *testing.T) {
	var events []runner.Event
	var started []string
	res, saw, err := Parse(
		strings.NewReader(stream),
		func(e runner.Event) { events = append(events, e) },
		func(id string) { started = append(started, id) },
	)
	if err != nil || !saw {
		t.Fatalf("Parse = %v, saw result %v", err, saw)
	}
	if !slices.Equal(started, []string{"abc"}) {
		t.Errorf("started with %v", started)
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
		{Type: runner.EventTool, Text: "Bash go test ./...", ID: "t1", Summary: "Run the tests",
			Detail: "go test ./...\nmore"},
		{Type: runner.EventResult, ID: "t1", Detail: "FAIL ward", Failed: true},
		{Type: runner.EventTool, Text: "Edit engine/ward.go", ID: "t2", Summary: "Edit ward.go",
			Detail: "{\n  \"file_path\": \"engine/ward.go\"\n}"},
		{Type: runner.EventResult, ID: "t2", Detail: "edited"},
	}
	if !slices.Equal(events, wantEvents) {
		t.Errorf("events = %+v", events)
	}
}

// TestParseCalls pins that each part of an assistant message reports its
// model call: the tokens it read, as the message's usage gives them, and
// the characters of that part.
func TestParseCalls(t *testing.T) {
	const calls = `{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":9,"cache_creation_input_tokens":6566,"cache_read_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":6566},"output_tokens":6},"content":[{"type":"thinking","thinking":"hmm"}]}}
{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":9,"cache_creation_input_tokens":6566,"cache_read_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":6566},"output_tokens":6},"content":[{"type":"text","text":"Hi there"}]}}
`
	var got []runner.Event
	if _, _, err := Parse(strings.NewReader(calls), func(e runner.Event) {
		if e.Type == runner.EventCall {
			got = append(got, e)
		}
	}, nil); err != nil {
		t.Fatal(err)
	}
	want := runner.Call{Input: 9, CacheWrite1h: 6566, CacheRead: 20}
	if len(got) != 2 || got[0].ID != "msg_1" || got[1].ID != "msg_1" {
		t.Fatalf("calls = %+v", got)
	}
	first, second := *got[0].Call, *got[1].Call
	if first.Wrote != 3 || second.Wrote != 8 {
		t.Errorf("wrote %d and %d, want 3 and 8", first.Wrote, second.Wrote)
	}
	first.Wrote = 0
	if first != want {
		t.Errorf("call = %+v, want %+v", first, want)
	}
}

func TestParseOutcomes(t *testing.T) {
	for line, want := range map[string]runner.Outcome{
		`{"type":"result","subtype":"error_max_turns","is_error":true}`:        runner.TurnLimit,
		`{"type":"result","subtype":"error_during_execution","is_error":true}`: runner.Failed,
		`{"type":"result","subtype":"success","is_error":true}`:                runner.Failed,
	} {
		res, _, _ := Parse(strings.NewReader(line), func(runner.Event) {}, nil)
		if res.Outcome != want {
			t.Errorf("%s: outcome %s, want %s", line, res.Outcome, want)
		}
	}
	if _, saw, _ := Parse(strings.NewReader(`{"type":"system"}`), func(runner.Event) {}, nil); saw {
		t.Error("Parse saw a result in a stream without one")
	}
}

func TestArgsResume(t *testing.T) {
	args, err := Args(runner.Spec{Resume: "abc"}, "/scratch")
	if err != nil ||
		!strings.Contains(strings.Join(args, " "), "--verbose --permission-mode dontAsk "+
			"--setting-sources project,local --strict-mcp-config "+
			"--exclude-dynamic-system-prompt-sections --resume abc") {
		t.Errorf("args = %q, %v", args, err)
	}
	if strings.Contains(args[len(args)-1], "promptCacheTtl") {
		t.Errorf("settings without a cache TTL = %s", args[len(args)-1])
	}
	args, _ = Args(runner.Spec{CacheTTL: "5m"}, "/scratch")
	if !strings.Contains(args[len(args)-1], `"promptCacheTtl":"5m"`) {
		t.Errorf("settings = %s", args[len(args)-1])
	}
}

// TestStopIsGraceful runs a fake claude that starts a session, leaves a child
// running, and exits on an interrupt as the real one does.
func TestStopIsGraceful(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"trap 'exit 130' INT TERM\n" +
		"sleep 600 & echo $! > " + child + "\n" +
		`echo '{"type":"system","subtype":"init","session_id":"abc"}'` + "\n" +
		"while :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := Runner{Binary: bin}.Run(ctx, runner.Spec{
			Dir:     dir,
			Started: func(id string) { started <- id },
		}, func(runner.Event) {})
		done <- err
	}()
	if id := <-started; id != "abc" {
		t.Fatalf("started %q", id)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Errorf("Run after a stop = %v", err)
		}
	case <-time.After(stopWait / 2):
		t.Fatal("the agent ignored the interrupt until it was killed")
	}
	pid, err := os.ReadFile(child)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(string(bytes.TrimSpace(pid)))
	if !gone(n, 3*time.Second) {
		t.Error("the agent's child outlived it")
	}
}

// gone waits up to d for a process to die. A killed process whose parent has
// already exited stays a zombie until init reaps it, which on some machines
// takes a while, so a zombie counts as dead.
func gone(pid int, d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || bytes.HasPrefix(bytes.TrimSpace(out), []byte("Z")) {
			return true
		}
	}
	return false
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
	if !settings.DisableBundledSkills ||
		settings.Hooks["PreToolUse"][0].Matcher != "Bash|Read|Grep|Glob|Edit|MultiEdit|Write|NotebookEdit" ||
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
	if err != nil || res.Outcome != runner.Completed || n != 5 {
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

func TestUnattended(t *testing.T) {
	env := strings.Join(unattended(runner.Spec{CommandTimeout: 30 * time.Second}), " ")
	if env != "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1 BASH_DEFAULT_TIMEOUT_MS=30000 BASH_MAX_TIMEOUT_MS=30000" {
		t.Errorf("env = %s", env)
	}
	if env := unattended(runner.Spec{}); len(env) != 1 {
		t.Errorf("without a timeout, env = %v", env)
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct{ tool, input, want string }{
		{"Grep", `{"pattern":"PromptSource"}`, "Search for PromptSource"},
		{"WebFetch", `{"url":"https://x"}`, "Fetch https://x"},
		{"Bash", `{"command":"ls"}`, "Bash ls"},
		{"Task", `{}`, "Task"},
	} {
		if got := describe(c.tool, []byte(c.input)); got != c.want {
			t.Errorf("describe(%s, %s) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
	long := strings.Repeat("x", detailBytes+10)
	if got := clipEnd(long); !strings.HasPrefix(got, "…\n") || len(got) > detailBytes+5 {
		t.Errorf("clipEnd kept %d bytes", len(got))
	}
	if got := clip(long); !strings.HasSuffix(got, "\n…") {
		t.Errorf("clip ends %q", got[len(got)-5:])
	}
}
