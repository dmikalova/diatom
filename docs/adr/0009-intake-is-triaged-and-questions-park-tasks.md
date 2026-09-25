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
- **The target is the focused goal by default.** Triage may propose moving an intake to a different goal.
- **Submitting a new goal is itself an intake**, so there is one input path for everything.
- **A question parks its task, and the session ends.** The worker moves on to other ready work. The answer makes the task ready again and is added to its context.
- **Grilling works in rounds.** Each round is one set of questions, and the answers queue the next round.
- **Questions from every goal appear in one pane**, so a question from a goal the human isn't looking at still reaches them.
