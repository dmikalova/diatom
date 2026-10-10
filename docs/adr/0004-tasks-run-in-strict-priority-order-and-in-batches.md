# 4. Tasks run in strict priority order and in batches

This decision records which work an agent picks up next and how much of it goes into one session.

## Context

Human feedback should get ahead of planned work, but stopping a session midway leaves a half-edited worktree. Every new agent session also pays again for its base context (instructions, relevant code and task setup). In practice, several well-scoped tasks in one session come out as good as one task per session, and cost less, but only while the session stays short: every step re-reads the whole context. Over vex's first 171 sessions, a session's steps read about 46k tokens each under 50 steps and 254k past 200, and a task cost $0.97 in a session of one task against $4.07 in a session of five.

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
  - **A batch goes on down a chain.** After its ready tasks, it takes the workstream's tasks that wait only on tasks already done or in the batch, in order. Planned work in a workstream usually depends on the task before it, so without this each task would pay for a session of its own. A chain may cross profiles when one covers the other (a model at least as capable, at least as much effort, and every tool and skill of the other): the whole batch then runs on the covering profile. A mechanical task followed by the implementation task that builds on it runs as one session on the implementation profile, since a second session would read its instructions and the code again, costing more than the cheaper model saves. The chain has a budget of context rather than only of tasks: once the session's context passes `chainContext` (default 100,000 tokens), marking a task done hands its tasks not yet started back to the queue, with no attempt counted, and the agent ends the session. A fresh session reads its base once, about 40k tokens, where each step of a long one reads everything so far again; the budget is where the saved re-reading of the chain's shared files stops paying for that, and the per-call costs the sessions now log can tune it.
  - **Among work of the same kind, the older goal goes first.** There is no pinning: work worth doing runs, work that should wait is parked, and work that depends on another goal waits for it (ADR 0003).
- **Planned work is split into small tasks at planning time**, so batches of planned work have the right size too.
- **Batches never span workstreams**, because a session runs in exactly one worktree.
- **Merges run beside the sessions, without an agent first.** A conflict task whose workstream has no merge in progress, such as one catching a goal up with its base, is tried by the harness: it merges the integration branch and the task's ref into the workstream, and when they go through, rerere settling any conflict already resolved once, and pass the gate, the task is done and the workstream integrated. This takes the workstream but no session, so it doesn't wait behind agents for `maxSessions` or a spent budget. A merge that conflicts or fails the gate is left in progress, and the next pass gives the task to an agent at its usual priority.
- **A merge in progress holds its workstream.** Nothing but a conflict or gate-repair task starts in a workstream mid-merge. One left mid-merge with neither, as a merge made by hand leaves it, gets a conflict task naming the conflicted files, so an agent finishes it rather than the workstream waiting with no one to unblock it.
- **A spent budget starts nothing new.** A repo can cap what its sessions cost today, over the last 7 days and over the last 30. Once one is spent, no session starts until the spending falls back under it, and the sessions running carry on, as they would for a new task. Each run of a session's agent counts once it ends, including one a stop cut short, shared among the days it worked by how many steps it took on each, so a session that goes on past midnight or resumes the next day counts on both. A session counts whatever goal it belonged to, so a finished goal's sessions count until they are 30 days old. A day's budget only holds new sessions until midnight, so a goal that costs more than a day's budget still finishes, over several days.
