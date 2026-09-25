# 3. Goals integrate continuously and never rebase mid-run

This decision records how a goal's work moves through branches and worktrees.

## Context

A goal such as "implement the new set" splits into workstreams with dependencies between them: engine, then cards, then web. Each workstream needs its own worktree so they don't interfere. Downstream workstreams need upstream work soon after it lands. The card workstream may also need engine changes of its own. Review is asynchronous (ADR 0001), so waiting for approval before sharing work would bring back the blocking diatom exists to remove. Review decisions are tied to commit SHAs, and rewriting history would break that link.

## Decision

**Each goal has an integration branch. Each workstream has a workstream branch off it, in its own worktree.**

- **Every commit that passes the gate is merged into the integration branch immediately**, without waiting for review.
- **A workstream merges the integration branch in before each task**, so downstream work always sees current upstream work.
- **Workstreams are scoped by purpose, not by path.** The card workstream may edit engine files. When two workstreams edit the same code, the harness creates a conflict-resolution task instead of blocking either one.
- **Nothing is rebased while a goal is running**, because commit SHAs anchor review decisions.
- **Revisions land as fixup commits**, one per original commit revised (`fixup! <original subject>`). Once the goal is done, the human is offered `git rebase --autosquash` and a split into stacked pull requests, or a direct push. Commits are sized to keep the gate passing, not to one change each.
- **Goals are isolated.** Two goals, even in the same repo, never share a branch or a worktree. A goal is planning, active, parked or done. Parking stops new tasks and leaves everything else in place, so resuming loses nothing.
