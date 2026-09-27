// Package claude runs agent sessions with the Claude Code CLI in headless mode
// (`claude -p --output-format stream-json`), the first runner backend
// (ADR 0006). It uses the Claude subscription, runs in any working directory,
// and loads the repo's own agent instructions and skills.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dmikalova/diatom/internal/gate"
	"github.com/dmikalova/diatom/internal/hook"
	"github.com/dmikalova/diatom/internal/runner"
)

// Runner runs sessions with the claude binary.
type Runner struct {
	// Binary is the claude executable; empty finds `claude` on PATH.
	Binary string
}

var _ runner.Runner = Runner{}

// Run implements runner.Runner.
func (r Runner) Run(
	ctx context.Context,
	spec runner.Spec,
	onEvent func(runner.Event),
) (runner.Result, error) {
	bin := r.Binary
	if bin == "" {
		bin = "claude"
	}
	scratch, err := os.MkdirTemp("", "diatom-claude-*")
	if err != nil {
		return runner.Result{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := writeScratch(spec, scratch); err != nil {
		return runner.Result{}, err
	}
	args, err := Args(spec, scratch)
	if err != nil {
		return runner.Result{}, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(append(os.Environ(), unattended(spec)...), spec.Env...)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	stopGracefully(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// A plain pipe rather than StdoutPipe, so the agent's exit can be waited
	// for while its output is still being read: a command the agent left in
	// the background holds the pipe open until it is killed.
	stdout, w, err := os.Pipe()
	if err != nil {
		return runner.Result{}, err
	}
	defer func() { _ = stdout.Close() }()
	cmd.Stdout = w
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		return runner.Result{}, fmt.Errorf("start %s: %w", bin, err)
	}
	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		// Whatever the agent left running goes with it.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waited <- err
	}()
	res, sawResult, perr := Parse(stdout, onEvent, spec.Started)
	_ = stdout.Close()
	werr := <-waited
	switch {
	case ctx.Err() != nil:
		return res, fmt.Errorf("%s stopped: %w", bin, ctx.Err())
	case perr != nil:
		return res, perr
	case sawResult:
		// claude exits non-zero when a session ends in error or at the turn
		// limit, which the result event already describes.
		return res, nil
	case werr != nil:
		return res, fmt.Errorf("%s: %w: %s", bin, werr, gate.Tail(stderr.String(), 20))
	default:
		return res, fmt.Errorf("%s ended without a result event", bin)
	}
}

// unattended is the environment for an agent nobody watches. Its session
// ends the moment it stops to wait, so a command it runs in the background
// would report back to nobody: every command runs in the foreground. A
// command that outlasts the timeout is stuck, and the agent may not extend
// it.
func unattended(spec runner.Spec) []string {
	env := []string{"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1"}
	if ms := spec.CommandTimeout.Milliseconds(); ms > 0 {
		env = append(env,
			"BASH_DEFAULT_TIMEOUT_MS="+strconv.FormatInt(ms, 10),
			"BASH_MAX_TIMEOUT_MS="+strconv.FormatInt(ms, 10))
	}
	return env
}

// A stopped agent is interrupted, terminated after stopTerm if it hasn't
// exited, and killed after stopWait.
const (
	stopTerm = 3 * time.Second
	stopWait = 15 * time.Second
)

// stopGracefully runs cmd in its own process group, so an interrupt at the
// terminal reaches diatom and not the agent, and makes cancelling its context
// interrupt the whole group, as Ctrl-C would, before terminating and then
// killing it. An agent started with interrupts ignored still stops quickly.
func stopGracefully(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := -cmd.Process.Pid
		time.AfterFunc(stopTerm, func() { _ = syscall.Kill(pgid, syscall.SIGTERM) })
		return syscall.Kill(pgid, syscall.SIGINT)
	}
	cmd.WaitDelay = stopWait
}

// Args returns the command-line arguments for a session, whose instructions
// and skills writeScratch has put in scratch. The prompt goes to stdin, so it
// never meets the argument length limit.
//
// A session gets no context the harness didn't pass in (ADR 0006): only the
// repo's own project settings load, not the user's, so none of the user's
// skills, plugins or hooks reach it; Claude Code's bundled skills are off; and
// only the configured MCP servers run.
func Args(spec runner.Spec, scratch string) ([]string, error) {
	p := spec.Profile
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "dontAsk",
		"--setting-sources", "project,local",
		"--strict-mcp-config",
	}
	if spec.Resume != "" {
		args = append(args, "--resume", spec.Resume)
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	if p.Effort != "" {
		args = append(args, "--effort", p.Effort)
	}
	if p.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(p.MaxTurns))
	}
	// --tools limits which built-in tools exist and --allowedTools
	// pre-approves them, patterns such as Bash(go test:*) included.
	var base []string
	for _, t := range p.Tools {
		name, _, _ := strings.Cut(t, "(")
		if !slices.Contains(base, name) {
			base = append(base, name)
		}
	}
	args = append(args, "--tools", strings.Join(base, ","))
	if len(p.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(p.Tools, ","))
	}
	for _, d := range spec.AddDirs {
		args = append(args, "--add-dir", d)
	}
	if spec.Instructions != "" {
		args = append(
			args,
			"--append-system-prompt-file",
			filepath.Join(scratch, "instructions.md"),
		)
	}
	if len(spec.Skills) > 0 {
		args = append(args, "--plugin-dir", filepath.Join(scratch, "plugin"))
	}
	if len(spec.MCPServers) > 0 {
		b, err := json.Marshal(map[string]any{"mcpServers": spec.MCPServers})
		if err != nil {
			return nil, fmt.Errorf("mcpServers: %w", err)
		}
		args = append(args, "--mcp-config", string(b))
	}
	settings := map[string]any{"disableBundledSkills": true}
	if hooks := hookSettings(spec.Hooks); hooks != nil {
		settings["hooks"] = hooks
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return append(args, "--settings", string(b)), nil
}

// writeScratch writes a session's instructions file and, for its skills, a
// plugin that holds only them. The skills load as diatom:<name>.
func writeScratch(spec runner.Spec, scratch string) error {
	if spec.Instructions != "" {
		if err := os.WriteFile(
			filepath.Join(scratch, "instructions.md"),
			[]byte(spec.Instructions),
			0o644,
		); err != nil {
			return err
		}
	}
	if len(spec.Skills) == 0 {
		return nil
	}
	plugin := filepath.Join(scratch, "plugin")
	if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0o755); err != nil {
		return err
	}
	manifest := `{"name":"diatom","description":"The skills diatom passes to this session","version":"0.0.0"}` + "\n"
	if err := os.WriteFile(
		filepath.Join(plugin, ".claude-plugin", "plugin.json"),
		[]byte(manifest),
		0o644,
	); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(plugin, "skills"), 0o755); err != nil {
		return err
	}
	for _, dir := range spec.Skills {
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			return fmt.Errorf("skill %s: %w", dir, err)
		}
		if err := os.Symlink(dir, filepath.Join(plugin, "skills", filepath.Base(dir))); err != nil {
			return err
		}
	}
	return nil
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Timeout is in seconds. The Stop hook runs the whole gate.
	Timeout int `json:"timeout,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

// hookSettings renders the hooks as Claude Code settings, or nil for none.
func hookSettings(h runner.Hooks) map[string][]hookMatcher {
	hooks := map[string][]hookMatcher{}
	if h.PreToolUse != "" {
		hooks["PreToolUse"] = []hookMatcher{
			{
				Matcher: strings.Join(append([]string{"Bash"}, hook.FileTools...), "|"),
				Hooks:   []hookCommand{{Type: "command", Command: h.PreToolUse}},
			},
		}
	}
	if h.Stop != "" {
		hooks["Stop"] = []hookMatcher{
			{Hooks: []hookCommand{{Type: "command", Command: h.Stop, Timeout: 3600}}},
		}
	}
	if len(hooks) == 0 {
		return nil
	}
	return hooks
}

// streamEvent is the part of a stream-json line Parse reads.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
			// A tool result's fields.
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
	// The result event's fields.
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
	Result    string  `json:"result"`
	NumTurns  int     `json:"num_turns"`
	CostUSD   float64 `json:"total_cost_usd"`
	Usage     struct {
		InputTokens   int `json:"input_tokens"`
		OutputTokens  int `json:"output_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
		CacheRead     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// Parse reads claude's stream-json output, calling onEvent for the agent's
// text and tool calls and started, when set, with the session's ID from its
// first event, and returns the session's result. sawResult is false when the
// stream ended before its result event.
func Parse(
	r io.Reader,
	onEvent func(runner.Event),
	started func(sessionID string),
) (res runner.Result, sawResult bool, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// A line that isn't JSON is claude printing something else, such
			// as a warning; it is not worth failing the session over.
			continue
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && ev.SessionID != "" && started != nil {
				started(ev.SessionID)
				started = nil
			}
		case "assistant", "user":
			contentEvents(ev, onEvent)
		case "result":
			sawResult = true
			res = runner.Result{
				SessionID: ev.SessionID,
				Outcome:   outcome(ev),
				Text:      ev.Result,
				Turns:     ev.NumTurns,
				Usage: runner.Usage{
					InputTokens:   ev.Usage.InputTokens,
					OutputTokens:  ev.Usage.OutputTokens,
					CacheCreation: ev.Usage.CacheCreation,
					CacheRead:     ev.Usage.CacheRead,
					CostUSD:       ev.CostUSD,
				},
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return res, sawResult, err
	}
	return res, sawResult, nil
}

// contentEvents reports what a message holds: the agent's words and tool
// calls, or the results the tools gave back.
func contentEvents(ev streamEvent, onEvent func(runner.Event)) {
	for _, c := range ev.Message.Content {
		switch {
		case ev.Type == "assistant" && c.Type == "text" && strings.TrimSpace(c.Text) != "":
			onEvent(runner.Event{Type: runner.EventText, Text: c.Text})
		case ev.Type == "assistant" && c.Type == "tool_use":
			onEvent(runner.Event{
				Type:    runner.EventTool,
				Text:    c.Name + " " + summarize(c.Input),
				ID:      c.ID,
				Summary: describe(c.Name, c.Input),
				Detail:  detail(c.Input),
			})
		case ev.Type == "user" && c.Type == "tool_result":
			onEvent(runner.Event{
				Type:   runner.EventResult,
				ID:     c.ToolUseID,
				Detail: resultText(c.Content),
				Failed: c.IsError,
			})
		}
	}
}

func outcome(ev streamEvent) runner.Outcome {
	switch {
	case ev.Subtype == "error_max_turns":
		return runner.TurnLimit
	case ev.IsError || ev.Subtype != "success":
		return runner.Failed
	default:
		return runner.Completed
	}
}

// detailBytes caps what a step keeps of a tool call's input or output.
const detailBytes = 8 << 10

// describe says in a few words what a tool call does: the description the
// agent gave it, or its tool and target.
func describe(name string, input json.RawMessage) string {
	var fields map[string]any
	_ = json.Unmarshal(input, &fields)
	str := func(k string) string { v, _ := fields[k].(string); return v }
	if d := str("description"); d != "" {
		return d
	}
	switch name {
	case "Read", "Edit", "MultiEdit", "Write", "NotebookEdit":
		if p := str("file_path"); p != "" {
			return name + " " + filepath.Base(p)
		}
	case "Grep":
		return "Search for " + str("pattern")
	case "Glob":
		return "Find " + str("pattern")
	case "WebFetch":
		return "Fetch " + str("url")
	case "WebSearch":
		return "Search the web for " + str("query")
	case "TodoWrite":
		return "Update the todo list"
	}
	if s := summarize(input); s != "" {
		return name + " " + s
	}
	return name
}

// detail is a tool call's whole input: a command as it is, anything else as
// indented JSON.
func detail(input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) == nil {
		if c, ok := fields["command"].(string); ok {
			return clip(c)
		}
	}
	var b bytes.Buffer
	if json.Indent(&b, input, "", "  ") != nil {
		return clip(string(input))
	}
	return clip(b.String())
}

// resultText is a tool result's text, which comes as a string or as blocks.
func resultText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return clipEnd(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return clipEnd(strings.Join(parts, "\n"))
}

// clip keeps the start of s, and clipEnd the end: an input says what it is
// first, and an output how it ended last.
func clip(s string) string {
	if len(s) > detailBytes {
		return strings.ToValidUTF8(s[:detailBytes], "") + "\n…"
	}
	return s
}

func clipEnd(s string) string {
	if len(s) > detailBytes {
		return "…\n" + strings.ToValidUTF8(s[len(s)-detailBytes:], "")
	}
	return s
}

// summarize shortens a tool call's input to the field that says what it does.
func summarize(input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "pattern", "path", "url", "description"} {
		if v, ok := fields[k].(string); ok {
			v, _, _ = strings.Cut(v, "\n")
			if len(v) > 120 {
				v = v[:120] + "…"
			}
			return v
		}
	}
	return ""
}
