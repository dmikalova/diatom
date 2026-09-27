# 4. Tasks run in strict priority order and in batches

This decision records which work an agent picks up next and how much of it goes into one session.

## Context

Human feedback should get ahead of planned work, but stopping a session midway leaves a half-edited worktree. Every new agent session also pays again for its base context (instructions, relevant code and task setup). In practice, several well-scoped tasks in one session come out as good as one task per session, and cost less.

## Decision

**Ready tasks run in a strict priority order. A new task never interrupts a running session; it is picked up at the next task boundary.**

1. **Gate repair**: a workstream branch fails its gate, for example after a merge.
2. **Conflict resolution.**
3. **Revisions**: rejections with comments.
4. **Triage** of intake (ADR 0009).
5. **Grilling** rounds (ADR 0010).
6. **Planned work**, in dependency order.

Triage and grilling come after revisions because a revision corrects work that later tasks are already building on. They come before planned work because they are the human's own input and often change what the planned work should be.

A task waiting on an answer from the human is not ready and is skipped (ADR 0009).

- **A session takes a batch**: as many ready tasks as fit, of the same kind, in the same workstream, capped at a configurable size (default 5). For revisions this means every pending revision in the workstream.
  - **A batch goes on down a chain.** After its ready tasks, it takes the workstream's tasks that wait only on tasks already done or in the batch, in order. Planned work in a workstream usually depends on the task before it, so without this each task would pay for a session of its own.
  - **Among work of the same kind, the older goal goes first.** There is no pinning: work worth doing runs, work that should wait is parked, and work that depends on another goal waits for it (ADR 0003).
- **Planned work is split into small tasks at planning time**, so batches of planned work have the right size too.
- **Batches never span workstreams**, because a session runs in exactly one worktree.
- **A spent budget starts nothing new.** A repo can cap what its sessions cost today, over the last 7 days and over the last 30. Once one is spent, no session starts until the spending falls back under it, and the sessions running carry on, as they would for a new task. Each run of a session's agent counts once it ends, including one a stop cut short, shared among the days it worked by how many steps it took on each, so a session that goes on past midnight or resumes the next day counts on both. A session counts whatever goal it belonged to, so a finished goal's sessions count until they are 30 days old. A day's budget only holds new sessions until midnight, so a goal that costs more than a day's budget still finishes, over several days.
