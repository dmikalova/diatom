# 3. Goals integrate continuously and never rebase mid-run

This decision records how a goal's work moves through branches and worktrees.

## Context

A goal such as "implement the new set" splits into workstreams with dependencies between them: engine, then cards, then web. Each workstream needs its own worktree so they don't interfere. Downstream workstreams need upstream work soon after it lands. The card workstream may also need engine changes of its own. Review is asynchronous (ADR 0001), so waiting for approval before sharing work would bring back the blocking diatom exists to remove. Review decisions are tied to commit SHAs, and rewriting history would break that link.

## Decision

**Each goal has an integration branch. Each workstream has a workstream branch off it, in its own worktree.**

- **The branches are `diatom/<goal>/integration` and `diatom/<goal>/ws/<workstream>`.** The integration branch can't simply be `diatom/<goal>`, because git can't hold a branch and branches under the same name.

- **Every commit that passes the gate is merged into the integration branch immediately**, without waiting for review.
- **A workstream merges the integration branch in before each task**, so downstream work always sees current upstream work.
- **Workstreams are scoped by purpose, not by path.** The card workstream may edit engine files. When two workstreams edit the same code, the harness creates a conflict-resolution task instead of blocking either one.
- **Nothing is rebased while a goal is running**, because commit SHAs anchor review decisions.
- **Revisions land as fixup commits**, one per original commit revised (`fixup! <original subject>`, or `fixup! <SHA>` when another commit of the goal has the same subject and autosquash could pick the wrong one). The agent marks each revision done as soon as it finishes it, and the harness splits the session's work into fixups at those points. The gate runs once, on the session's final files: a fixup's own tree is never merged anywhere, and autosquash moves it beside its original anyway. Commits are sized to keep the gate passing, not to one change each.
- **A done goal is laid out for landing, then pushed or opened as a stack of pull requests.** `diatom goal done` replays the goal's commits onto the base branch's tip without the merges, squashing each fixup into the commit it revises as `git rebase --autosquash` would. The commits are grouped by workstream in dependency order, the goal's ADRs with the first, and each group is one pull request of a stack on `diatom/<goal>/pr/<workstream>`. Grouping reorders commits, so when a workstream built on another's later change to the same code, the goal becomes one pull request in the order the work was done instead. A merge that a conflict or gate-repair task fixed holds changes no commit has, so a last commit carries them: the layout always ends with the files the integration branch passed the gate with, merged with whatever the base branch gained since. The gate runs on each pull request's tip, because one may pass only with those after it. `diatom goal finish -prs` then pushes the branches and opens the stack with `gh`, and `-push` pushes straight to the base branch, never forcing. The integration branch and its review records are left as they were.
- **Goals are isolated.** Two goals, even in the same repo, never share a branch or a worktree. A goal is planning, active, parked or done. Parking stops new tasks and leaves everything else in place, so resuming loses nothing.
