# 5. The harness owns git and the gate

This decision records the split between what agents do and what the harness does.

## Context

Every commit has to pass the repo's checks, so downstream workstreams and reviews never build on broken code. The link from each task to its commits is what makes revisions possible, so a single trusted component has to record it. Repos may already forbid agents from running git; vex's `AGENTS.md` does. The repo's hooks can't be trusted to enforce the checks either: a global `core.hooksPath` setting once silently disabled lefthook in every repo.

## Decision

**Agents only edit files. The harness does every git operation and runs the gate itself.**

- **The gate is a configurable command per repo** (`mage check` for vex). It covers lint, build and tests. Formatting steps in the gate may rewrite files, so the harness runs it and then commits the result.
- **Agent sessions get two Claude Code hooks from diatom:**
  - **PreToolUse** blocks git commands that change the repository, such as commit, merge, checkout and reset.
    It also blocks backgrounding a command (`&`, `nohup`, `setsid`) and `sleep`. Every command is stopped at the command time limit, and a headless session can't be woken when a background command ends, so an agent that backgrounds a long command and sleeps on its log is only working around the limit. It should run a narrower check instead and leave the whole gate to the Stop hook.
  - **Stop** runs the gate before the session may end. On failure, the output goes back to the agent, which keeps fixing in the same session. That is the cheapest retry, because nothing has to be reloaded.
- **A gate that runs too long is stuck, and fails.** A good gate run takes seconds, so after `commandTimeout` (30 seconds by default) the gate is stopped and fails, with a note that something in it hangs. Every command an agent runs has the same limit, which the agent can't extend.
- **After 3 failed gate attempts** (configurable), the failed work is stashed and the task is retried once. The retry keeps the task's profile and model and runs one effort level higher, capped at xhigh, with the gate's failure output added to the task's text. It never switches to a bigger model: a retry should think a little more with more context, not become a far costlier agent. A task that two sessions leave unfinished is retried the same way. If the retry still fails, the task is parked as a question to the human with the failure output. A session whose work diatom itself can't settle, such as a gate that can't run, sends its tasks back to the queue. The second time in a row, the task is parked as a question with the error instead, since another session would only repeat it. With no gate configured, no implementation session starts at all; its tasks are parked on a question saying to set one. A commit is never made while the gate is failing, and the gate is never skipped.
- **Commit messages come from a separate call on the cheapest profile.** It is given the staged diff and the task titles, and must produce a Conventional Commits message. Any text before the first commit line is stripped, the message is checked with the repo's commitlint when there is one, and it is retried once on failure. This follows the `coco` script in dotfiles.
- **Merge conflicts** are resolved by an agent that edits the conflicted files like any others. The harness completes the merge.
