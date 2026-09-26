# 5. The harness owns git and the gate

This decision records the split between what agents do and what the harness does.

## Context

Every commit has to pass the repo's checks, so downstream workstreams and reviews never build on broken code. The link from each task to its commits is what makes revisions possible, so a single trusted component has to record it. Repos may already forbid agents from running git; vex's `AGENTS.md` does. The repo's hooks can't be trusted to enforce the checks either: a global `core.hooksPath` setting once silently disabled lefthook in every repo.

## Decision

**Agents only edit files. The harness does every git operation and runs the gate itself.**

- **The gate is a configurable command per repo** (`mage check` for vex). It covers lint, build and tests. Formatting steps in the gate may rewrite files, so the harness runs it and then commits the result.
- **Agent sessions get two Claude Code hooks from diatom:**
  - **PreToolUse** blocks git commands that change the repository, such as commit, merge, checkout and reset.
  - **Stop** runs the gate before the session may end. On failure, the output goes back to the agent, which keeps fixing in the same session. That is the cheapest retry, because nothing has to be reloaded.
- **After 3 failed gate attempts** (configurable), the failed work is stashed and the task is retried once. The retry keeps the task's profile and model and runs one effort level higher, capped at xhigh, with the gate's failure output added to the task's text. It never switches to a bigger model: a retry should think a little more with more context, not become a far costlier agent. A task that two sessions leave unfinished is retried the same way. If the retry still fails, the task is parked as a question to the human with the failure output. A commit is never made while the gate is failing, and the gate is never skipped.
- **Commit messages come from a separate call on the cheapest profile.** It is given the staged diff and the task titles, and must produce a Conventional Commits message. Any text before the first commit line is stripped, the message is checked with the repo's commitlint when there is one, and it is retried once on failure. This follows the `coco` script in dotfiles.
- **Merge conflicts** are resolved by an agent that edits the conflicted files like any others. The harness completes the merge.
