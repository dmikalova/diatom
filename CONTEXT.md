# diatom

A harness that runs coding agents continuously through a priority queue of work while a human reviews their commits asynchronously and streams corrections back in.

## Language

### Planning

**Goal**:
A unit of intent submitted by the human, such as "implement the new set". A goal owns its plan, workstreams, integration branch and tasks, and ends as one or more pull requests.
_Avoid_: prompt, job, project

**Parked**:
A goal state in which none of the goal's tasks are started. The goal's branches, worktrees and queue are kept as they are, so resuming it loses nothing. The other goal states are planning, active and done.
_Avoid_: paused, suspended, inactive

**Intake**:
Free-form input from the human, such as a new goal or a handful of playtest notes, waiting to be sorted into goals and tasks.
_Avoid_: inbox, submission, notes, todo

**Triage**:
The task that sorts an intake into new tasks, a new goal or questions back to the human.
_Avoid_: classification, routing

**Plan**:
A goal broken into workstreams, tasks and the dependencies between them, signed off by the human before work starts.
_Avoid_: roadmap, breakdown

**Workstream**:
A named line of work within a goal, such as engine, cards or web, with its own branch and worktree. A workstream is scoped by purpose, not by path, so it may edit any file.
_Avoid_: track, lane, stream

### Work

**Task**:
One unit of agent work within a workstream. Its kind is one of planned, revision, conflict resolution, gate repair or grilling.
_Avoid_: job, ticket, item

**Profile**:
The kind of agent a piece of work needs, such as planning, implementation, mechanical or commit message, which maps to a model and effort level.
_Avoid_: tier, model class, agent type

**Batch**:
A set of ready tasks of the same kind in the same workstream that one agent session works through together.
_Avoid_: chunk, bundle

**Revision**:
A task created from rejected hunks and their comments, asking the agent to rework code it already committed.
_Avoid_: fix, feedback task, correction

**Gate**:
The check command every commit must pass before it lands, such as `mage check`.
_Avoid_: CI, lint step, green check

### Review

**Review decision**:
The human's verdict on one hunk of one commit: approve, reject or defer. Decisions are fixed to that commit and never reopen when later commits change the same code.
_Avoid_: verdict, status, vote

**Comment**:
A human note attached to lines in a hunk. Comments on a rejected hunk become the content of a revision.
_Avoid_: feedback, note, remark

**Defer**:
A review decision meaning "not now". The hunk stays in the review queue behind unreviewed hunks and creates no agent work.
_Avoid_: skip, snooze, postpone

### Conversation

**Question**:
Something an agent needs the human to decide. The task that raised it is parked until it is answered.
_Avoid_: prompt, query, blocker

**Answer**:
The human's reply to a question. It makes the parked task ready again.
_Avoid_: response, reply

### Branches

**Integration branch**:
The branch that collects all of a goal's work. Every commit that passes the gate is merged in as soon as it lands.
_Avoid_: feature branch, main branch

**Workstream branch**:
A branch off the integration branch where one workstream commits. It merges the integration branch in before each task.
_Avoid_: sub-branch, topic branch
