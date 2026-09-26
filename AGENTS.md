# AGENTS.md

Conventions for working on diatom itself. The design is in `docs/adr/` and the
vocabulary in `CONTEXT.md`; use its terms (goal, workstream, task, batch, gate,
profile) in code and comments.

## Validating

Run `mage ci:fix && mage ci:check` before calling work done. It must print
`ALL GREEN`. The targets come from project-standards' shared `ci` package.

## Layout

| Package                  | Role                                                     |
| ------------------------ | -------------------------------------------------------- |
| `cmd/diatom`             | The CLI: scheduler, goals, tasks, the task tool, hooks   |
| `internal/harness`       | The scheduler loop and the lifecycle of one batch        |
| `internal/schedule`      | Pure priority and batching decisions (ADR 0004)          |
| `internal/queue`         | Goals, tasks and questions as files (ADR 0002)           |
| `internal/session`       | The files one agent session shares with the harness      |
| `internal/git`           | Every git operation the harness owns (ADRs 0003, 0005)   |
| `internal/hook`          | The PreToolUse git block and the Stop gate (ADR 0005)    |
| `internal/runner`        | The runner interface, and `claude/` its first backend    |
| `internal/commitmsg`     | Commit messages on the cheapest profile (ADR 0005)       |
| `internal/review`        | Hunks, review decisions and the review queue (ADR 0001)  |
| `internal/reviewui`      | The native reviewer, a Bubble Tea app (ADR 0008)         |
| `internal/config`        | The config walk-up (ADR 0007)                            |

## Rules

- **Only the harness moves task files.** Anything an agent reports goes through
  the task tool into its session directory, and the harness applies it.
- **Never commit while the gate fails, and never skip it.** Failed work is
  stashed, not committed.
- **Never rewrite history on diatom's branches.** Review decisions are anchored
  to commit SHAs.
- **Test the harness against real git.** The harness tests build real
  repositories and stand in only for the agent and the gate.
