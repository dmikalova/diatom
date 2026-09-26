# diatom

A harness that runs coding agents continuously through a priority queue of work
while a human reviews their commits asynchronously and streams corrections back
in. The design is in [`docs/adr/`](docs/adr/) and the vocabulary in
[`CONTEXT.md`](CONTEXT.md).

## Status

The scheduler core runs:

- the Markdown queue (ADR 0002)
- integration and workstream branches (ADR 0003)
- priority order and batching (ADR 0004)
- the gate and git hooks (ADR 0005)
- the Claude Code runner (ADR 0006)
- one scheduler per machine (ADR 0007)
- questions that park tasks (ADR 0009)
- sessions with only explicit context (ADR 0011)
- the reviewer, with revisions landing as fixups (ADRs 0001 and 0008)
- the zellij workspace and its panes (ADR 0007)
- triage of intake, and grilling with plan sign-off (ADRs 0009 and 0010)

## Install

```bash
go install github.com/dmikalova/diatom/cmd/diatom@latest
```

diatom keeps its state in `.diatom/` inside each repo, which must be ignored
through the global excludes file:

```bash
echo '.diatom/' >> ~/.config/git/ignore
```

## Use

```bash
cd ~/Code/github.com/dmikalova/vex
echo 'gate: mage ci:check' > .diatom/config.yaml

# A new goal is grilled first: answer its questions, then sign off its plan.
diatom goal new new-set -title "Implement the new set" < goal.md
diatom goal plan new-set
diatom goal approve new-set

# Or skip grilling and write the tasks by hand.
diatom goal new hotfix -ws engine -active
diatom task add -goal hotfix -ws engine "Fix ward stacking" < fix.md

diatom workspace    # everything below in one zellij session, scheduler included
diatom run          # the scheduler; Ctrl-C finishes running sessions, twice stops them
diatom status       # every goal in every known repo
diatom review       # approve, reject or defer each hunk the agents committed
diatom questions    # what the agents need decided
diatom answer new-set 0001 "Ward does not stack."
diatom goal done new-set   # refuses while hunks are unreviewed or deferred
diatom goal finish new-set -prs   # or -push, straight to the base branch
```

In the reviewer, `a`, `r` and `d` approve, reject and defer the hunk on screen,
`c` comments on the line under the cursor, `u` steps back through earlier
decisions, and `v` shows a fixup folded into the commit it revises. A
rejection's comments become a revision task within seconds, and the agent's fix
lands as a `fixup!` commit that comes back for review.

A done goal is laid out on `diatom/<goal>/final`: its commits replayed onto the
base branch without the merges, fixups squashed into the commits they revise,
and split into one pull request per workstream on `diatom/<goal>/pr/<ws>`, each
stacked on the one before. `goal finish -prs` pushes those branches and opens
the stack with `gh`; `-push` pushes the lot straight to the base branch.

Each workstream gets a worktree under `.diatom/goals/<goal>/worktrees/` on the
branch `diatom/<goal>/ws/<workstream>`. Every commit that passes the gate merges
into `diatom/<goal>/integration`.

## Configure

Config is merged from `.diatom/config.yaml` in the repo and each parent
directory up to your home, then `~/.config/diatom/config.yaml`. The closest file
wins. The built-in defaults are in
[`internal/config/defaults.yaml`](internal/config/defaults.yaml).

```yaml
gate: mage ci:check               # required; the check every commit must pass
gateAttempts: 3                   # gate failures sent back before a retry at more effort
maxSessions: 1                    # sessions at once in this repo
maxBatch: 10                      # tasks per session
commitCheck: project-standards commit-msg   # lints a commit message file
instructions: [~/AGENTS.md]       # appended to every agent's system prompt
mcpServers:                       # the only MCP servers agents get
  docs: { command: docs-mcp }
```

Agent sessions load none of your own Claude Code settings, skills, plugins or
MCP servers (ADR 0011). They get the instruction files above, the repo's own
`AGENTS.md`, `CLAUDE.md` and project settings, and the skills their profile
lists.

Only `~/.config/diatom/config.yaml` may set these:

```yaml
machineSessions: 3
profiles:
  implementation: { model: opus, effort: medium, maxTurns: 300 }
  planning: { skills: [grilling] }  # names in ~/.claude/skills, or paths
```
