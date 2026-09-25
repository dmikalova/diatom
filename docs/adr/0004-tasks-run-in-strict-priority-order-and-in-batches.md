# 4. Tasks run in strict priority order and in batches

This decision records which work an agent picks up next and how much of it goes into one session.

## Context

Human feedback should get ahead of planned work, but stopping a session midway leaves a half-edited worktree. Every new agent session also pays again for its base context (instructions, relevant code and task setup). In practice, several well-scoped tasks in one session come out as good as one task per session, and cost less.

## Decision

**Ready tasks run in a strict priority order. A new task never interrupts a running session; it is picked up at the next task boundary.**

1. **Gate repair**: a workstream branch fails its gate, for example after a merge.
2. **Conflict resolution.**
3. **Revisions**: rejections with comments.
4. **Planned work**, in dependency order.

A task waiting on an answer from the human is not ready and is skipped (ADR 0009).

- **A session takes a batch**: as many ready tasks as fit, of the same kind, in the same workstream. For revisions this means every pending revision in the workstream, capped at a configurable size (default 10).
- **Planned work is split into small tasks at planning time**, so batches of planned work have the right size too.
- **Batches never span workstreams**, because a session runs in exactly one worktree.
