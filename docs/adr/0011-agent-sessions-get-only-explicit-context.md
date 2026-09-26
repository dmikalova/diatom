# 11. Agent sessions get only explicit context

This decision records what context an agent session starts with.

## Context

A headless Claude Code session loads everything the human's interactive sessions do:

- every skill in `~/.claude/skills`
- Claude Code's bundled skills
- user plugins, hooks and MCP servers

The first real diatom session wrote about 140K tokens of cache for a trivial task, and most of that was context the task never used. It also let the human's personal setup change how agents behave, invisibly. Claude Code does not read `AGENTS.md` files at all, so the instructions diatom's repos rely on were the one thing missing.

## Decision

**A session starts with only the repo's own project settings and what diatom passes in explicitly.**

- **The user's settings are not loaded**, so none of their skills, plugins or hooks reach the agent. The repo's project settings and its `CLAUDE.md` still load.
- **Claude Code's bundled skills are off**, and no MCP server runs unless one is configured.
- **Instructions are passed in.** The configured instruction files (by default `~/AGENTS.md`) and the worktree's root `AGENTS.md` are appended to the system prompt. The prompt lists any nested `AGENTS.md` files for the agent to read before working in their directories.
- **Skills are chosen per profile.** A profile lists the skills its agents may load, such as `grilling` for planning, and only those are passed in.
- **Connectors are configured per repo.** MCP servers are set in the config walk-up (ADR 0007) and passed in.

Nothing reaches a session by accident, and every addition is visible in config. The cost is that a skill which would help a task is unavailable until it is configured.
