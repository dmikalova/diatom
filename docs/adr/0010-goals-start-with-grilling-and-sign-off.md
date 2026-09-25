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
- **During the run, agents may add tasks to existing workstreams without approval.** Those tasks show up in the status pane. For clear-cut work, an agent either does it or records it as a task. Anything that needs a design decision goes back to the human as a new grilling round.
- **Adding a workstream or changing dependencies needs the human's approval.**
- **Where ADRs go and what format they use is configurable** through the config walk-up (ADR 0007), so personal and work repos can follow different standards:
  - If a repo has an ADR directory, ADRs are committed there on the integration branch and reviewed like code.
  - Otherwise they stay in the goal's directory, and the human is offered the option to commit them when the goal is done.
