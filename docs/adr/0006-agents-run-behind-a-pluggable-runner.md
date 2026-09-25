# 6. Agents run behind a pluggable runner, first backed by Claude Code

This decision records how diatom runs agent sessions and chooses a model for each piece of work.

## Context

The candidates were:

- **tenzing-agent-harness**: the closest Go harness in design, but it has no license, a single author, and pins its tools to the process working directory.
- **Pi and pi-hunk**: TypeScript, and built around synchronous review.
- **A hand-written loop on anthropic-sdk-go**: full control over context trimming and prompt caching, but every tool, permission and piece of session storage has to be built, and usage is billed per API token.
- **The Claude Code CLI in headless mode** (`claude -p --output-format stream-json`): can use the Claude subscription, runs in any working directory, can resume sessions, and loads the repo's own agent instructions and skills. vex's card-implementation skills come with it for free.

Cost matters, but most of the savings come from batching (ADR 0004), model choice and short sessions, and those work the same with any backend.

## Decision

**The scheduler runs agents only through a small runner interface.** The first implementation drives `claude -p`.

- **The interface is intentionally narrow**:
  - start a batch in a worktree, with a prompt, a profile and the hooks
  - stream progress events
  - return the result (the tasks completed, notes, questions raised, and token usage)

  A second backend, such as a direct API loop or tenzing if it gets a license, only has to implement this interface. The interface itself doesn't need to be more general than that.
- **Each batch starts a fresh session**, so history never accumulates from one batch to the next. Prompts refer to files instead of pasting their contents. Each profile has its own list of allowed tools and a turn limit.
- **Every task has a profile**, which the config maps to a model, an effort level, a list of allowed tools and a turn limit:
  - **planning**: grilling, triage, planning, writing ADRs
  - **implementation**: planned work and revisions
  - **mechanical**: gate repair and conflict resolution
  - **commit message**

  Each task kind has a default profile, the planner can choose a different one for a given task, and failed tasks move up one profile (ADR 0005). More profiles can be added as needed.
- **Token usage is recorded per task.** That data decides whether a backend with more control over context trimming is worth its cost.
