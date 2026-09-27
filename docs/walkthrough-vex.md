# Running diatom on vex

This walks through running diatom on vex: sending it work, answering what it
asks, reviewing what it builds, and landing it. You run every step yourself.

## 0. One-time setup

```sh
go install github.com/dmikalova/diatom/cmd/diatom@latest
diatom version        # prints the release, such as v0.4.0
gh auth status        # diatom opens PRs and reads CI checks with gh
```

- `claude` needs to be logged in, and `~/go/bin` needs to be on your `PATH`.
- Right after a release, `@latest` can still resolve to the one before it for
  up to half an hour, while Go's module proxy catches up. Name the version, as
  in `@v0.4.0`, to get it straight away.
- `~/.config/diatom/config.toml`, from your dotfiles, sets `autoUpdate = true`,
  and `mage ci:check` as the gate of every Go repo. diatom installs each new
  release when it opens, and while it runs installs one in the background and
  says so in the footer: `U` restarts on it, resuming the sessions.
- Your global git ignore already lists `.diatom/`, which diatom requires before
  it starts.
- In Ghostty, `keybind = performable:super+c=copy_to_clipboard` lets cmd+c
  copy from diatom's window when nothing is selected in the terminal.

## 1. Prepare vex

1. **Commit what the agents should see.** Agents work from committed `main`,
   so anything they should see has to be committed first.
2. **Optionally create `.diatom/config.toml`.** It's ignored, so it stays
   local:

   ```toml
   maxSessions = 2               # agent sessions at once; raise it once you trust it
   autoApprove = ["*_test.go"]   # hunks in these files are approved for you
   ```

   vex needs no gate of its own: it has a `go.mod`, so it gets the Go gate
   from your home config, `mage ci:check`. Without one, diatom would ask for it
   when it opens. The gate is `ci:check`, not `ci:fix`, because it must only
   check. vex's AGENTS.md forbids git operations, which matches diatom: the
   harness makes every commit.

   vex's `ci:check` takes about 12 seconds warm, and over a minute on a cold
   build cache. A gate still running after 2 minutes is stuck: diatom stops
   it and counts it as failed. Each command an agent runs is stopped after 30
   seconds, so agents run narrow checks and leave the whole gate to diatom.
   Set `gateTimeout` or `commandTimeout` in this file to change either.

## 2. Open diatom

```sh
cd ~/Code/github.com/dmikalova/vex && diatom
```

The window runs vex's scheduler, which starts sessions as soon as work is
ready. The nav down the left lists:

- **Next**: what waits on you, one item at a time.
- **Intake**: what triage is sorting, and its questions.
- **The goals**, each with a glyph for where it stands and counts of its open
  questions (`?2`) and hunks to review (`±14`). Finished goals fold away at the
  bottom.
- **The footer**: what the work has cost, the sessions running, and anything
  wrong with the scheduler. Clicking it, or `L`, opens the scheduler's log.
- **The intake box**, at the foot.

The main pane shows what the nav selects. Tab moves between the nav, the parts
of the main pane and the intake box, and the mouse clicks and scrolls. `h`
hides the nav, and dragging its edge resizes it. Every menu opens its selection
with `enter` or `space`, and `esc` backs out one level.

Quitting with `q` or ctrl+c, or closing the terminal, suspends every session
within seconds with its files as they are. The next time diatom opens, it
resumes each agent's own Claude session where it stopped. Nothing runs while
diatom is closed. A second diatom on vex only views.

## 3. Send it work

Break the work up by what lands together. Areas that edit the same code belong
in one goal, because conflicts inside a goal are fixed as the work goes.
Conflicts between goals only surface when the second one lands. Each goal's
workstreams become one stacked PR each.

Press `i` and type what you want, then `enter` to send it. `shift+enter`
starts a new line:

```text
Split effectGlyphs by glyph family, so the last gocognit exclusion can go.
Then close the effect catalog behind a RulesBearing marker, as ADR 0018
decided; the effect glyph split waits for it.
```

What the main pane shows goes with it as a clue to where it belongs, so an
idea you have while answering a question can be sent straight away: triage
works out whether it belongs to that goal, another, or a new one. Triage, an
agent, sorts the intake into the repo's goals, each with a one-line
description of what it is for. A goal whose work the intake already decides
comes with its plan, waiting for your sign-off; the rest are grilled first.
Open Intake to watch triage work. Anything it can't decide comes to Next.

A goal can wait for others: nothing of it runs, grilling included, until they
are finished (step 7). The nav marks it **blocked** (`⊘`), and its page names
the goals it waits for. Its branch then starts from `main` with them landed,
so it never overlaps them.

## 4. Next: answer questions, then sign off each plan

Each planning goal gets grilling rounds: read-only planning sessions on Opus
that ask questions and then hand in a plan. Everything they ask comes to Next,
with plans to sign off first, then questions, each level in the nav's order of
goals.

An item of Next has three parts, and tab moves through them:

1. **What it is about**: the goal's title, where it stands, what it is for, and
   which task asked. `enter` here opens the goal's page, with its brief, plan
   and earlier questions and answers; `esc` comes back.
2. **The item**: the question or plan in full. `↑`/`↓` scroll it, and `space`
   scrolls half a page.
3. **The answer**: `enter` sends it and moves on to the first item left.

For a plan:

- To correct it, write what to change and press `enter`. The plan is set aside
  and a new round starts with your feedback.
- To approve it, press `enter` twice with nothing typed, or `s` twice on the
  goal's page. This queues the tasks and commits any ADR drafts from grilling,
  which then come up for review like any other commit.

With the terminal in the background, a notification says when Next has
something again after having nothing.

## 5. While the work runs

A goal's page shows its task counts, open questions, hunks waiting for review,
its cost so far, and a `▶ <workstream>` line with the latest step of any
running session. An active goal reads **queued** while its ready work waits for
a free session, **reviewing** once every task is done and hunks are left, and
**ready to finish** once those are reviewed too. Its tasks open to the Claude
sessions that worked on them, those to their steps, and those to their full
command and output.

A freed session goes to the most urgent kind of work first: fixes, then
revisions, triage, grilling and planned work. Among work of the same kind it
goes to the goal highest in the nav. A session takes up to 5 tasks of one
workstream at once, following a chain of tasks that depend on each other, so
related work shares one agent's context. Every prompt lists the repo's other
goals, so an agent knows what they cover and how far they have got.

What happens without you:

- Every commit that passes the gate is merged into the goal's integration
  branch right away, and downstream workstreams pick it up before their next
  task.
- A commit that fails the gate is sent back to the agent. If it keeps failing,
  it's retried once with more thinking, then becomes a question to you.
- Merge conflicts between workstreams become conflict-resolution tasks.

What needs you:

- **Questions** from implementation agents come to Next too. Answering one
  unblocks its task.
- **Review, whenever you like**, in Next once the questions are answered, or
  with `r` on a goal's page:
  - `a`, `r` and `d` approve, reject and defer the hunk on screen, and `n` and
    `p` move between hunks.
  - `c` comments on the line under the cursor, and `u` steps back through
    earlier decisions.
  - Rejecting with a comment becomes a revision task within seconds. The fix
    comes back as a `fixup!` commit, and `v` shows it folded into the original.
  - Review doesn't hold up the agents, but you have to review everything before
    step 6.
  - Hunks in files matching `autoApprove` are approved for you and never reach
    the reviewer. Its header counts them.
- **Controls:** `p` on a goal's page parks or resumes it. Nothing new starts
  while parked, and nothing is lost.

To try the work yourself, such as running `mage web` to see it in a browser,
check the goal's integration branch out beside vex:

```sh
git worktree add --detach ../vex-check diatom/<goal>/integration
cd ../vex-check && mage web
```

## 6. Land a goal

Once every task is done and every hunk reviewed, the goal comes up in Next,
with what it changes and the goals waiting for it. Choose one, with `enter`
twice or its key twice:

- **`P`, Merge it into `main`.** You've already reviewed every hunk inside
  diatom, so this is the simple path for a solo repo, and vex's CI/CD runs on
  the push.
- **`F`, Open its stacked pull requests**, one per workstream, each based on
  the one before. Use this when you want CI per PR. Merge them bottom-up using
  **"Create a merge commit"**. Squash or rebase merges rewrite the lower PR's
  commits, so the next PR up would show them all again.
- **`d`, Mark it done and lay it out**, to land later with `P` or `F`. `D`
  on its page marks it done even with work left.

First the goal catches up with `main`, fetched from origin. What `main` gained
since the goal started is merged in. When that conflicts, landing stops there:
an agent merges `main` into the goal's last workstream and resolves the
conflicts, the goal is active again, and the resolution comes to Next as hunks
to review. Once they're reviewed, the goal is ready to finish again.

Then diatom replays the goal's commits onto `main` without the merge commits,
with each fixup squashed into its target, runs the gate on each PR's tip, and
lands it. If the workstreams can't be put in stack order, you get one PR, and
the goal's page says why. What landing came to stays on screen until you press
a key or move on.

## 7. Wait for it to land

The goal stays in the nav until it's **finished**. Every two minutes the
scheduler checks whether `origin/main` holds all of the goal's changes and
whether vex's checks on it have passed. A failing check keeps the goal in view
and shows the failing checks in red. When it finishes, the goal folds away
under Finished, its worktrees are removed, and the goals waiting for it start.
Then `git pull` in vex. The `diatom/<goal>/…` branches are kept; delete them
whenever you like.

## Things to watch

- **Gate runs.** vex's `ci:check` runs every linter and the 100%-coverage tests,
  in each worktree, on every commit. Watch the first few gate runs in the
  scheduler's log before raising `maxSessions`.
- **Cost.** diatom has no spending cap; `maxSessions` is the only limit.
  Grilling runs on Opus at high effort, and implementation on Opus at medium.
  The footer and the window's title show the total, and each goal's page its
  own and each task's.
