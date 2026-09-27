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
- one window per repo, with its scheduler inside (ADR 0007)
- questions that park tasks (ADR 0009)
- sessions with only explicit context (ADR 0011)
- the reviewer, with revisions landing as fixups (ADRs 0001 and 0008)
- triage of intake, and grilling with plan sign-off (ADRs 0009 and 0010)
- landing done goals as a stack of pull requests (ADR 0003)
- suspending and resuming sessions, and updating itself (ADR 0012)

## Install

```bash
go install github.com/dmikalova/diatom/cmd/diatom@latest
```

Installed this way, diatom can keep itself on the latest release: set
`autoUpdate = true` in `~/.config/diatom/config.toml`. A diatom built from a
checkout never updates itself.

diatom keeps its state in `.diatom/` inside each repo, which must be ignored
through the global excludes file:

```bash
echo '.diatom/' >> ~/.config/git/ignore
```

## Use

[`docs/walkthrough-vex.md`](docs/walkthrough-vex.md) walks through a whole run
on vex, from goals to landing them.

diatom works in the repo it is started in, from anywhere inside it. For two
repos, run two diatoms.

```bash
cd ~/Code/github.com/dmikalova/vex
diatom
```

That opens diatom's window, which runs the repo's scheduler too. Quitting with
`q` or ctrl+c, or closing the terminal, suspends the running sessions, and they
carry on the next time diatom opens: nothing runs while it is closed. A second
diatom on the same repo only views, and answering and reviewing still work
there.

The nav down the left lists Next, the intake and the repo's goals, with the
intake box at its foot. The main pane shows what the nav selects.

- **Next** is what waits on you, across every goal, one item at a time: plans
  to sign off, then questions, then goals ready to finish, then hunks to
  review, each in the nav's order of goals. Above each item is what its goal is
  for; enter there opens the goal. Answering moves on to the first item left.
- **The intake box** takes anything you want done, such as "Turn
  docs/todo.md into goals, one per section". Triage, an agent, sorts it into
  the repo's goals: new goals, tasks on existing ones, feedback for a goal
  being planned, or questions back to you. What the main pane showed goes with
  it, as a clue. A new goal is grilled first: answer its questions in Next,
  then sign off its plan there. Work the intake already decides skips grilling
  and waits for your sign-off.
- **A goal's page** says where it stands and what it is for, then offers its
  actions, its review, its plan and its tasks, each task opening to its Claude
  sessions and their steps.

Tab moves between the nav, the parts of the main pane and the intake box, and
the mouse clicks and scrolls. Outside a text box:

- `i` goes to the intake box, and `h` hides the nav; dragging its edge resizes
  it
- `L` opens the scheduler's log, as clicking the nav's footer does
- `y` copies what has the keyboard, as cmd+c does in Ghostty with
  `keybind = performable:super+c=copy_to_clipboard`
- `U` restarts on a new release or build, once the footer says one is installed

With the terminal in the background, a notification says when something comes
to wait on you.

In review, `a`, `r` and `d` approve, reject and defer the hunk on screen, `c`
comments on the line under the cursor, `n` and `p` move between hunks, `u` steps
back through earlier decisions, and `v` shows a fixup folded into the commit it
revises. A rejection's comments become a revision task within seconds, and the
agent's fix lands as a `fixup!` commit that comes back for review.

A goal ready to finish comes up in Next. Finishing it with `d` lays it out on
`diatom/<goal>/final`: its commits replayed onto the base branch without the
merges, fixups squashed into the commits they revise, and split into one pull
request per workstream on `diatom/<goal>/pr/<ws>`, each stacked on the one
before. `F` pushes those branches and opens the stack with `gh`, and `P` pushes
the lot straight to the base branch. The goal stays in the nav until it is
finished: diatom watches the base branch on the remote and finishes the goal
once it holds all of the goal's changes and its checks pass.

Stopping never loses work. Each agent stops within seconds with its files as
they are, and the next time diatom opens it carries the agent's own Claude
session on where it stopped.

Each workstream gets a worktree under `.diatom/goals/<goal>/worktrees/` on the
branch `diatom/<goal>/ws/<workstream>`. Every commit that passes the gate merges
into `diatom/<goal>/integration`.

## Configure

Config is TOML, merged from `.diatom/config.toml` in the repo and each parent
directory up to your home, then `~/.config/diatom/config.toml`. The closest file
wins. The built-in defaults are in
[`internal/config/defaults.toml`](internal/config/defaults.toml).

Every repo needs a gate, the check every commit must pass. A repo that sets
none gets the one `[gates]` names for its kind of project: go (a `go.mod`),
node (`package.json`), deno (`deno.json`), rust (`Cargo.toml`) or python
(`pyproject.toml`). With neither, diatom asks for one when it opens and saves
it in the repo's `.diatom/config.toml`.

```toml
gate = "mage ci:check"            # this repo's gate
gateAttempts = 3                  # gate failures sent back before a retry at more effort
gateTimeout = "2m"                # a gate running longer is stuck, and fails
commandTimeout = "30s"            # an agent's command running longer is stopped
autoApprove = ["*_test.go"]       # files whose hunks are approved without review
maxSessions = 1                   # sessions at once in this repo
maxBatch = 5                      # tasks per session, following chains of dependent tasks
commitCheck = "project-standards commit-msg"   # lints a commit message file
instructions = ["~/AGENTS.md"]    # appended to every agent's system prompt

[gates]                           # the gate of each kind of repo that sets none
go = "mage ci:check"
node = "npm test"

[mcpServers.docs]                 # the only MCP servers agents get
command = "docs-mcp"
```

Agent sessions load none of your own Claude Code settings, skills, plugins or
MCP servers (ADR 0011). They get the instruction files above, the repo's own
`AGENTS.md`, `CLAUDE.md` and project settings, and the skills their profile
lists.

Only `~/.config/diatom/config.toml` may set these:

```toml
autoUpdate = true                 # install new releases; U restarts on one

[profiles.implementation]
model = "opus"
effort = "medium"
maxTurns = 300

[profiles.planning]
skills = ["grilling"]             # names in ~/.claude/skills, or paths
```
