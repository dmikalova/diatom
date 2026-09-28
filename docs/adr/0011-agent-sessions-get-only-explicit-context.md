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
- **Instructions are passed in.** The configured instruction files, then the `AGENTS.md` of every directory from the home directory down to the repo's (the repo's own read from the worktree), are appended to the system prompt, each file once. The walk mirrors the config's (ADR 0007): a file above the repo, such as one for all of an organisation's repos, applies to every repo under it. The prompt lists the nested `AGENTS.md` files below the repo's root for the agent to read before working in their directories, rather than appending them: each applies only to its own directory, and most tasks never work there.
- **Skills are chosen in config.** `skills`, set anywhere in the walk-up, lists the skills every session may load, and a profile adds its own, such as `grilling` for planning. Only those are passed in, with the `Skill` tool to load them.
- **Connectors are configured per repo.** MCP servers are set in the config walk-up (ADR 0007) and passed in.
- **The prompt cache is kept as long as it is worth it.** A session's steps are seconds apart, so implementation, revision and fix sessions keep their prompt cache five minutes, which costs 1.25× the input price to write, not the hour's 2×. Triage and grilling keep theirs an hour: their next round is the same conversation carried on once the human answers, so a round answered within about 55 minutes resumes the last one's agent session, told what changed, and reads back what it already found for a tenth of the price of finding it again. Past that the cache is gone, and a fresh session costs less. Implementation work isn't carried on this way: diatom commits it after each session, and the new git status in the resumed context misses the cache anyway. The cwd, environment and git status go in the first message rather than the system prompt (`--exclude-dynamic-system-prompt-sections`), so every session of a repo shares one cached system prompt.
- **The repo's other goals are passed in.** Every prompt lists them with their state, progress, what they wait for and a one-line description, which triage writes when it starts a goal. `diatom task goals <name>` shows one in full: its plan summary, workstreams and tasks. An agent otherwise went looking in `.diatom/` or took the repo's own notes as other work's status, so the PreToolUse hook keeps file tools out of `.diatom/` (ADR 0005).

Nothing reaches a session by accident, and every addition is visible in config. The cost is that a skill which would help a task is unavailable until it is configured.
