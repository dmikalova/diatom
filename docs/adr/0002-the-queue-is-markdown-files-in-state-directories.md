# 2. The queue is Markdown files in state directories

diatom's runtime state lives as plain files in the repository it works on, not in a database or a queue service.

## Context

diatom runs locally with only a few agent sessions at a time, so Redis-backed queues such as Asynq, or Temporal, are far more machinery than needed. The queue has to survive crashes and restarts. Its tasks are mostly prose that both humans and agents read and add to. SQLite would make metadata queries easy, but task files could no longer be read or edited directly.

## Decision

**Each task is one Markdown file with YAML frontmatter, and a task's state is the directory it sits in.**

- **State lives under `$REPO/.diatom/`**, which is ignored through the global git excludes file (`~/.config/git/ignore`), so repos need no changes. Each goal has its own directory, `.diatom/goals/<goal>/`, holding its plan, config, queue and worktrees.
- **State changes are atomic renames** between `pending/`, `active/`, `blocked/` and `done/`. After a crash, a task is in exactly one state.
- **Frontmatter** carries the id, kind, profile, priority, workstream, dependencies and origin (the plan, a review decision, or a question).
- **Only the harness moves task files.** Agents add notes to a task's body through a tool. This rules out races between the agent and the scheduler.
- **Finished tasks stay until the goal is done.** A task's link to its commits is what turns a later rejection into a revision with the right context. When the goal is done, its directory and worktrees are deleted. Git history and ADRs are the lasting record.
- **Per-repo config stays in the ignored directory** (`.diatom/config.yaml`), so diatom never has to be committed into a work repository.
