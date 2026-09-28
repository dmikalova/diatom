# 9. Intake is triaged, and questions park tasks

This decision records how free-form human input enters the queue and how agents ask the human for decisions.

## Context

New work doesn't always come as a well-formed goal. After a playtest, the human may have a handful of notes spread across several cards, and in the middle of a run they may think of something unrelated. vex handled this with an agent-owned `docs/todo-agent.md`, which became hard to manage: agents ordered it themselves, and it had no connection to review or scheduling. Agents also regularly need decisions from the human, and waiting for one shouldn't stop other work.

## Decision

**Everything the human sends is an intake, and every intake goes to triage: an agent that sorts it into the repo's goals.** The human has two channels. Intake is how they send the agents anything to sort out. Questions are how the agents come back to them, from triage and from tasks alike.

- **Every intake has a triage task**, whether it was typed into the intake box or, before a comment became a rejection (ADR 0001), left while approving a hunk. What the window showed when it was sent goes with it, the goal and a short excerpt, as a clue to where it belongs, not a rule: an idea can come to the human while they answer a question about something else. Triage tasks live in a hidden goal of the repo's own, which holds their questions too. Sorting input into goals is triage's alone, but any session may start a goal the human explicitly asks it for, such as in answering its question: the goal records the goal it came from, and one whose title is taken already isn't started twice. Such a note still waiting holds its goal's landing until triage has sorted it, since triage may add work to that goal.
- **Triage reads the repo and all of its goals, and sorts the intake into any of:**
  - tasks on the workstreams of any active or parked goal
  - feedback for a goal still in planning: a plan waiting for sign-off is set aside, and grilling starts another round with it
  - one or more new goals, each grilled before work starts (ADR 0010), or signed off straight away when the intake already decides the work. Triage gives each a one-line description of what it is for, which other goals' agents and the human see.
  - questions back to the human when something is unclear
- **Triage reports through the task tool.** A task for a goal that is done, on a workstream the goal doesn't have, or after a task that doesn't exist becomes a question instead, because adding a workstream or changing dependencies needs the human (ADR 0010). A task for a goal in planning becomes feedback for its grilling. An intake is filed as done once its triage is, and not while triage waits on a question.
- **Planning sessions can't end without reporting.** A question or plan written only in the agent's reply reaches nobody, so the Stop hook of triage and grilling sends the agent back, twice at most, until each task is asked about or handed in.
- **Asking for a new goal is itself an intake**, so there is one input path for everything. "Turn `docs/todo-agent.md` into goals, one per section" is one intake, which triage turns into several goals.
- **A question parks its task, and the session ends.** The worker moves on to other ready work. The answer makes the task ready again and is added to its context.
- **Manual steps are a question too.** What only the human may do, such as applying infrastructure, is handed to them as numbered steps that park the task the same way. Their answer says the steps are done, or what happened instead, so a failed apply comes back to the agent as context rather than as a guess. The goal shows 👤 while it waits on them.
- **Grilling works in rounds.** Each round is one set of questions, and the answers queue the next round.
- **Questions from every goal and from triage appear in Next** (ADR 0007), so a question from a goal the human isn't looking at still reaches them.
