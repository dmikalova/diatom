# 9. Intake is triaged, and questions park tasks

This decision records how free-form human input enters the queue and how agents ask the human for decisions.

## Context

New work doesn't always come as a well-formed goal. After a playtest, the human may have a handful of notes spread across several cards, and in the middle of a run they may think of something unrelated. vex handled this with an agent-owned `docs/todo-agent.md`, which became hard to manage: agents ordered it themselves, and it had no connection to review or scheduling. Agents also regularly need decisions from the human, and waiting for one shouldn't stop other work.

## Decision

**Everything the human types into the input pane is an intake, and every intake gets a triage task on the planning profile.**

- **Triage sorts the intake into one or more of:**
  - tasks on workstreams of an existing goal
  - a new goal, which needs sign-off (ADR 0010)
  - questions back to the human when something is unclear
- **The target is the focused goal by default.** Triage may start a new goal from part of an intake instead of adding to the focused one.
- **With only a repo focused, an intake is a new goal.** It starts in planning with a grilling task and needs no triage.
- **Triage reports through the task tool**: tasks it adds to the goal's workstreams, goals it starts, and questions. A task on a workstream the goal doesn't have, or after a task that doesn't exist, becomes a question instead, because adding a workstream or changing dependencies needs the human (ADR 0010). An intake is filed as done once its triage is.
- **An intake for a goal still in planning is feedback for its grilling.** A plan waiting for sign-off is set aside and grilling starts another round with the feedback.
- **Planning sessions can't end without reporting.** A question or plan written only in the agent's reply reaches nobody, so the Stop hook of triage and grilling sends the agent back, twice at most, until each task is asked about or handed in.
- **Submitting a new goal is itself an intake**, so there is one input path for everything.
- **A question parks its task, and the session ends.** The worker moves on to other ready work. The answer makes the task ready again and is added to its context.
- **Grilling works in rounds.** Each round is one set of questions, and the answers queue the next round.
- **Questions from every goal appear in one pane**, so a question from a goal the human isn't looking at still reaches them.
