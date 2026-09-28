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

The nav down the left lists Next, the intake and the repo's goals, then a
menu with the finished goals, the scheduler's log and the spending, and the
intake box at its foot. The main pane shows what the nav selects. The footer
shows what sessions cost today, over the last 7 days and over the last 30, as
`D$22 · W$80 · M$200`, red where it spends the repo's budget. Clicking it opens
the spending: what each of the last 30 days cost, each day opening to what
each goal and session spent on it.

- **Next** is what waits on you, across every goal, one item at a time: goals
  ready to finish with the hunks finishing them brought, then plans to sign
  off, then questions, then the other hunks to review, each in the nav's order
  of goals. A goal ready to finish can instead get more work, or wait until
  later. Above each item is what its goal is
  for; enter there opens the goal. Answering moves on to the first item left.
- **The intake box** takes anything you want done, such as "Turn
  docs/todo.md into goals, one per section". Triage, an agent, sorts it into
  the repo's goals: new goals, tasks on existing ones, feedback for a goal
  being planned, or questions back to you. What the main pane showed goes with
  it, as a clue. A new goal is grilled first: answer its questions in Next,
  then approve its plan there, `a` twice, or comment on it with `c`. Work the
  intake already decides skips grilling and waits for your approval.
- **A goal's page** says where it stands and what it is for, then offers its
  running sessions, each opening straight to its steps, its actions, its
  review, its plan and its tasks, each task opening to its Claude sessions and
  their steps. A click does what `enter` does.

Tab moves between the nav, the parts of the main pane and the intake box, and
the mouse clicks and scrolls. Outside a text box:

- `i` goes to the intake box, and `h` hides the nav; dragging its edge resizes
  it
- `L` opens the scheduler's log, which is also in the menu at the nav's foot
  and opens from its footer
- dragging in the main pane selects text within it, and copies it on release
- `y` copies the selection, or else what has the keyboard, as cmd+c does in
  Ghostty with `keybind = performable:super+c=copy_to_clipboard`
- `U` restarts on a new release or build, once the footer says one is installed

With the terminal in the background, a notification says when something comes
to wait on you.

In review, `a`, `r` and `d` approve, reject and defer the hunk on screen, `c`
rejects it with a comment on the line under the cursor, `b` goes back through
earlier decisions, and on a fixup, `v` shows it folded into the commit it
revises. A rejection's comments become a revision task within seconds, and the
agent's fix lands as a `fixup!` commit that comes back for review.

A goal ready to finish comes up in Next. `P` merges it into the base branch,
and `F` opens it as a stack of pull requests instead, one per workstream on
`diatom/<goal>/pr/<ws>`, each stacked on the one before; `d` only marks it
done, to land later. Either way, the goal first catches up with the base branch,
fetched from the remote: what the base gained is merged in, and when that
conflicts, an agent resolves it and the resolution comes back for review before
the goal can land. The goal's commits are then replayed onto the base branch's
tip on `diatom/<goal>/final`, without the merges and with fixups squashed into
the commits they revise. When the base changed the goal's own code, so the
commits no longer apply on its tip as they are, they're rebased onto it: git
merges what it can, and an agent on the mechanical profile settles each
conflict git leaves, keeping both the base's change and the commit's. Before the goal
lands, what the agent changed comes to Next for review, just its change from
the files as git left them; rejecting a hunk settles that commit again with
your comments. If the result differs from the merge you reviewed, a last commit
brings it to that merge. `P` merges into the remote's base branch, then
fast-forwards your local one when that can't lose anything: never over commits
of your own, and never over uncommitted changes to the files that landed.
Nothing is stashed; otherwise you pull when ready. The base's history stays linear. A saved
landing with a merge or an unsigned commit is never reused. Every commit diatom makes is signed when
`commit.gpgSign` is set. While a goal lands, its page shows each step and the
gate's output as they come, with nothing else offered, and Next moves on. A landing that fails leaves the goal active. One whose commits fail the gate
never lands: an agent makes the gate pass, with its output shown under the
notice, and the goal comes back to Next. It stays in the nav until it is
finished: diatom watches the base branch on the remote and finishes the goal
once it holds all of the goal's changes and its checks pass.

What an action comes to stays under it until you press a key, click, or move
to another view.

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

diatom reads the config once, when it starts. A change takes effect the next
time it starts, which `U` does without losing any running session, so a
mistake saved mid-run can't stop the scheduler: diatom refuses to start on it
instead, naming the file and line.

Every repo needs a gate, the check every commit must pass. A repo that sets
none gets the one `[gates]` names for its kind of project: go (a `go.mod`),
node (`package.json`), deno (`deno.json`), rust (`Cargo.toml`) or python
(`pyproject.toml`). With neither, diatom asks for one when it opens and saves
it in the repo's `.diatom/config.toml`.

```toml
gate = "mage ci:check"            # this repo's gate
gateAttempts = 3                  # gate failures sent back before a retry at more effort
fix = "mage ci:fix"               # formats and regenerates before the gate; agents never run it
gateTimeout = "5m"                # the fix and gate running longer are stuck, and fail
commandTimeout = "2m"             # an agent's command running longer is stopped
autoApprove = ["*_test.go"]       # files whose hunks are approved without review
maxSessions = 1                   # sessions at once in this repo
maxBatch = 10                     # tasks per session, following chains of dependent tasks
commitCheck = "project-standards commit-msg"   # lints a commit message file
instructions = ["~/notes/go.md"] # more files for every agent's system prompt
skills = ["grill-me", "grilling"] # skills every session may load: names in ~/.claude/skills, or paths
editor = "nvim"                   # what the reviewer's o opens a hunk's file in, at its line
land = "merge"                    # how goals land: "merge" into their base, "prs"; unset offers both

[budget]                          # dollars; once one is spent, nothing new starts
day = 50                          # today
week = 200                        # the last 7 days
month = 600                       # the last 30 days

[gates]                           # the gate of each kind of repo that sets none
go = "mage ci:check"
node = "npm test"

[fixes]                           # the fix of each kind of repo that sets none
go = "mage ci:fix"

[mcpServers.docs]                 # the only MCP servers agents get
command = "docs-mcp"
```

Agent sessions load none of your own Claude Code settings, skills, plugins or
MCP servers (ADR 0011). They get the instruction files above, the `AGENTS.md`
of every directory from your home directory down to the repo's, the repo's
`CLAUDE.md` and project settings, the MCP servers above and the skills above
and in their profile. Any config file in the walk-up can set `skills`,
`instructions` and `mcpServers`, `~/.config/diatom/config.toml` for every repo;
a nearer file's list replaces a further one's, while MCP servers add up by
name. A nested `AGENTS.md`, such as `internal/engine/AGENTS.md`, isn't loaded:
the prompt names it for the agent to read before working in that directory.

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
