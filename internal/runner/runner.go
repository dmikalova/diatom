// Package runner is the narrow interface the scheduler runs agents through
// (ADR 0006): start a batch in a worktree with a prompt, a profile and the
// hooks, stream progress events, and return how the session ended and what it
// spent. A second backend only has to implement Runner.
//
// The tasks a session completed, its notes and its questions come back
// through the task tool rather than the Result, because the tool is
// backend-independent: the Claude Code backend exposes it as the
// `diatom task` command, and an API backend would call the same session
// functions directly.
package runner

import (
	"context"

	"github.com/dmikalova/diatom/internal/config"
)

// Runner runs one agent session.
type Runner interface {
	// Run runs the session to its end, calling onEvent for each progress
	// event. It returns an error only when the session could not run; a
	// session that ran out of turns or ended in error is a Result.
	Run(ctx context.Context, spec Spec, onEvent func(Event)) (Result, error)
}

// Spec is what one session runs.
type Spec struct {
	// Dir is the working directory, the workstream's worktree.
	Dir string
	// AddDirs are directories outside Dir the agent may read and edit, such
	// as the one holding its task files.
	AddDirs []string
	// Prompt is the session's instructions.
	Prompt string
	// Profile chooses the model, effort, tools and turn limit.
	Profile config.Profile
	// Env is added to the agent's environment.
	Env []string
	// Hooks are the commands the agent must run at its hook points; empty
	// ones are not installed.
	Hooks Hooks
	// Instructions are appended to the agent's system prompt.
	Instructions string
	// Skills are the skill directories the agent may load. The agent gets no
	// other skills, and no other context than the repo's own settings.
	Skills []string
	// MCPServers are the MCP servers the agent may use, in Claude Code's
	// mcpServers format. No others are loaded.
	MCPServers map[string]any
}

// Hooks are shell commands run at the agent's hook points.
type Hooks struct {
	// PreToolUse runs before each Bash command and may deny it.
	PreToolUse string
	// Stop runs when the agent tries to end the session and may send it back
	// to work.
	Stop string
}

// EventType is what an Event reports.
type EventType string

// The event types.
const (
	EventText EventType = "text"
	EventTool EventType = "tool"
)

// Event is one piece of a session's progress.
type Event struct {
	Type EventType `json:"type"`
	// Text is the agent's words, or for a tool the tool's name and a short
	// summary of its input.
	Text string `json:"text"`
}

// Outcome is how a session ended.
type Outcome string

// The outcomes.
const (
	// Completed means the agent ended the session itself.
	Completed Outcome = "completed"
	// TurnLimit means the session hit the profile's turn limit.
	TurnLimit Outcome = "turn-limit"
	// Failed means the agent errored out.
	Failed Outcome = "failed"
)

// Result is how one session ended and what it spent.
type Result struct {
	SessionID string  `json:"sessionID"`
	Outcome   Outcome `json:"outcome"`
	// Text is the agent's final message.
	Text  string `json:"text"`
	Turns int    `json:"turns"`
	Usage Usage  `json:"usage"`
}

// Usage is the tokens and cost of a session.
type Usage struct {
	InputTokens   int     `json:"inputTokens"`
	OutputTokens  int     `json:"outputTokens"`
	CacheCreation int     `json:"cacheCreation"`
	CacheRead     int     `json:"cacheRead"`
	CostUSD       float64 `json:"costUSD"`
}
