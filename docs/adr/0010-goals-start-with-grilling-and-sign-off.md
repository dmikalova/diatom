# 10. Goals start with grilling and sign-off

This decision records how a goal becomes a plan and how far agents may change a plan after that.

## Context

A goal such as "implement the new set" hides decisions the agent can't make alone:

- which mechanics are new
- how they fit the engine
- what order the workstreams need to go in

If the agent guesses, it produces a lot of work that later has to be revised. Requiring approval for every task added during a run would bring the blocking back.

## Decision

**A new goal is grilled before any work starts. Grilling produces a plan and ADRs where they're warranted, and the human signs the plan off.**

- **The plan** contains the workstreams, the dependencies between them, and small tasks, each with a profile. Signing it off moves the goal from planning to active.
  - Grilling hands the plan in as YAML through the task tool, which checks it on the spot so the agent fixes mistakes in the same session.
  - A plan waiting for sign-off heads Next (ADR 0007), since it holds a whole goal up. The human signs it off there or on the goal's page, or writes what to change, which sends it back to grilling. Each task then depends on the tasks it names and on every task of the workstreams its own workstream depends on.
  - Grilling reads the code in a detached checkout of the integration branch. Anything written there is thrown away.
- **Work the human has already decided skips grilling, never sign-off.** When an intake decides the workstreams and tasks itself, triage hands in the plan along with the new goal (ADR 0009). The goal starts in planning with the plan waiting for sign-off. A plan that doesn't hold up against the config sends the goal to grilling instead, which starts from the draft.
- **During the run, agents may add tasks to existing workstreams without approval.** Those tasks show up on the goal's page. For clear-cut work, an agent either does it or records it as a task. Anything that needs a design decision goes back to the human as a new grilling round.
- **Agents order the work themselves and never ask the human about it.** The human cares that the work gets done, cleanly and with the least effort, not in what sequence. Grilling orders workstreams and tasks by dependency, then simplest and lowest-risk first. Triage orders goals the same way, making later ones wait for earlier ones. A choice an agent can reason through is made and recorded as a note, not put to the human for approval. Questions are for structure, architecture, behaviour and what is unclear in what is wanted.
- **Adding a workstream or changing dependencies needs the human's approval.**
- **Where ADRs go and what format they use is configurable** through the config walk-up (ADR 0007), so personal and work repos can follow different standards:
  - If a repo has an ADR directory, ADRs are committed there on the integration branch and reviewed like code. Grilling drafts them beside the goal, and sign-off commits them.
  - Otherwise they stay in the goal's directory, and the human is offered the option to commit them when the goal is done.
