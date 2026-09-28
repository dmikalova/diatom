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
- A Nerd Font draws its own 🤖 and ☑, which Ghostty can pick over the color
  emoji once diatom redraws them.
  `font-codepoint-map = U+1F916,U+2611=Apple Color Emoji` keeps them emoji.

## 1. Prepare vex

1. **Commit what the agents should see.** Agents work from committed `main`,
   so anything they should see has to be committed first.
2. **Optionally create `.diatom/config.toml`.** It's ignored, so it stays
   local:

   ```toml
   maxSessions = 2               # agent sessions at once; raise it once you trust it
   autoApprove = ["*_test.go"]   # hunks in these files are approved for you

   [budget]                      # dollars; once one is spent, nothing new starts
   day = 50
   week = 200
   month = 600
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

- **Next**: what waits on you, one item at a time. Its second line counts
  what waits by kind, then the agents running (🤖) and the git work under way
  (🔀).
- **Intake**: what triage is sorting, and its questions.
- **The goals**, each on two lines: a glyph for where it stands and its
  title, then the one thing about it that matters most now, such as
  `2 questions`, `48 hunks to review` or `blocked`. The glyph is 👤 while
  the goal waits on steps you do by hand, 🤖 while an agent works on it, and
  🔀 while diatom commits a session's work or lands it.
- **A menu** at the foot: ☑️ Finished lists the finished goals, the latest
  first; 📒 the scheduler's log, which `L` opens too; and 💰 Spending shows
  what each of the last 30 days cost against the budget, with `enter` on a day
  showing what each goal and session spent on it.
- **The footer**: what the sessions cost today, over the last 7 days and over
  the last 30, as `D$22 · W$80 · M$200`, and anything wrong with the
  scheduler. Clicking the cost opens the spending, and the rest the
  scheduler's log.
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
are finished (step 7). The nav marks it **blocked** (🔗) with the goal it waits for, and its page names
the goals it waits for. Its branch then starts from `main` with them landed,
so it never overlaps them. A branch grilling made before they landed has
`main` merged in once as the goal stops waiting.

## 4. Next: answer questions, then approve each plan

Each planning goal gets grilling rounds: read-only planning sessions on Opus
that ask questions and then hand in a plan. Everything they ask comes to Next,
with plans to approve before questions, each level in the nav's order of
goals. A goal ready to finish comes before both (step 6).

An item of Next has three parts, and tab moves through them:

1. **What it is about**: the goal's title, where it stands, what it is for, and
   which task asked. `enter` here opens the goal's page, with its brief, plan
   and earlier questions and answers; `esc` comes back.
2. **The item**: the question or plan in full. `↑`/`↓` scroll it, `space`
   scrolls half a page down and `shift+space` half a page up.
3. **The answer**: `enter` sends it and moves on to the first item left.

A plan is decided on the way a hunk is, with its keys while you read it or
from the list of what can be done below it:

- **`a` twice approves it.** This queues the tasks and commits any ADR drafts
  from grilling, which then come up for review like any other commit.
- **`c` comments on it**: write what to change and press `enter`. The plan is
  set aside and a new round starts with your comment.
- **`l`, Later**, puts it behind everything else waiting on you.

On a planning goal's page, `a` opens its plan the same way, and after it
anything else the goal waits on you for.

Some work needs you to do something by hand that agents mustn't, such as
running `tofu apply` or setting a secret. The agent finishes what it can,
then hands you numbered steps, which come to Next with the questions, marked
👤. Do them, then press `enter` twice with nothing typed to say they are
done, or write what happened instead, such as the error you got. Either way
the task picks up again with your reply.

With the terminal in the background, a notification says when Next has
something again after having nothing.

## 5. While the work runs

A goal's page shows its task counts, open questions, hunks waiting for review,
its cost so far, and a `▶ <workstream>` line with the latest step of each
running session. `enter` on that line opens the session's steps as they
come, under the model and effort it runs at, such as `opus · high`, and `esc`
comes back. Agents work without narrating: each task done carries a short
summary, which its page shows. A click on it, on an action or on a task does
what `enter` does, and dragging selects text instead. An active goal reads **queued** while its ready work waits for
a free session, **reviewing** once every task is done and hunks are left, and
**ready to finish** once those are reviewed too. Its tasks open to the Claude
sessions that worked on them, those to their steps, and those to their full
command and output.

A freed session goes to the most urgent kind of work first: fixes, then
revisions, triage, grilling and planned work. Among work of the same kind it
goes to the goal highest in the nav. A session takes up to 10 tasks of one
workstream at once, following a chain of tasks that depend on each other, so
related work shares one agent's context. A chain that goes from a mechanical
task to an implementation one runs all of it on the implementation profile.
Every prompt lists the repo's other
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
  - `a` approves the hunk on screen. `d` defers it, which asks you again once
    the rest are done.
  - `c` comments on the line under the cursor, in a box that grows to ten
    lines: `shift+enter` adds a line, and `enter` sends it and moves on. A
    comment can ask for a change, or just ask a question: the goal's agent
    reads it with the code around it, changes the code where asked, and
    answers a question in a note on the task.
  - `r` rejects the hunk without a comment: the agent takes it out, or does it
    another way where the task needs it. To say how it should change,
    comment instead. `b` goes back through earlier decisions.
  - `o` opens the hunk's file in nvim at the line under the cursor, from the
    worktree of the workstream that made it, so it has the code as that work
    does. nvim takes the terminal until you quit it, and the review is where
    you left it; sessions keep running meanwhile. What you change there goes
    into that workstream's next commit, so `editor = "nvim -R"` opens it
    read-only. `editor` sets another command.
  - A comment or rejection becomes a revision task within seconds. The fix
    comes back as a `fixup!` commit, and `v` shows it folded into the original.
  - Review doesn't hold up the agents, but you have to review everything before
    step 6.
  - Hunks in files matching `autoApprove` are approved for you and never reach
    the reviewer. Its header counts them.
  - An image's hunks show it as it was and as it is, side by side above the
    diff: SVG, PNG, JPEG, GIF, WebP and BMP, in Ghostty or kitty. A binary
    file is one hunk, approved or rejected as a whole.
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
  the one before. Use this when you want CI per PR. Merge them bottom-up. A
  repo that requires linear history, as vex does, can only squash or rebase
  them, which rewrites the lower PR's commits, so the next PR up shows them
  all again: `P` suits such a repo better. vex's `.diatom/config.toml` sets
  `land = "merge"`, so its goals offer only `P`; `land = "prs"` offers only
  `F`.
- **`d`, Mark it done**, to land later with `P` or `F`.
- **`m`, More work**: write what it still needs, and `enter` adds it as a task
  on the goal's last workstream. The goal is active again, and comes back to
  finish once that work is done and reviewed.
- **`l`, Later**, puts it behind everything else waiting on you.

Next doesn't change the item on screen while you look at it, but each thing
you do there, an answer or a hunk decided, moves on to the most urgent item
left: a plan handed in while you review another goal comes up after the hunk
you're on. From a goal's page, `a` goes through that goal's items alone, in
the same order: its questions, its plan, its hunks and its landing. The page
comes back once none is left, or with `esc`.

First the goal catches up with `main`, fetched from origin. What `main` gained
since the goal started is merged in. When that conflicts, landing stops there:
an agent merges `main` into the goal's last workstream and resolves the
conflicts, the goal is active again, and the resolution comes to Next as hunks
to review, beside the goals ready to finish rather than behind the other
reviews, since they hold up the landing. Once they're reviewed, the goal is
ready to finish again.

Then diatom replays the goal's commits onto `main` without the merge commits,
with each fixup squashed into its target, runs the gate on each PR's tip, and
lands it. When `main` changed the goal's own code, the commits are rebased
onto `main`'s tip instead: git merges what it can, and an agent settles each
conflict git leaves, keeping both `main`'s change and the commit's, with the
merge you reviewed as its guide. If the result differs from that merge, a last
commit brings it there, so `main`'s history stays linear and every commit on
it is signed. The page's log shows each conflict as the agent settles it. What
the agent changed then comes to Next for review, beside the goals ready to
finish, before anything lands, just
its edits to the files git left conflicted; rejecting a hunk has it settle
that commit again with your comments. `P` merges into `origin/main`, then
fast-forwards your local `main` unless it has commits of its own or your
uncommitted changes touch what landed; nothing is stashed. While it lands, the goal's page shows each step and the
gate's output as they come, with nothing else offered, and Next moves on to
the next thing. Landing another goal while one lands queues it behind: Next
moves on, the nav shows it ⏳ waiting to land, and it starts once the one
before it ends. The queue lasts while diatom is open. A landing that fails
leaves the goal active, with
the reason on screen. If the workstreams can't be put in stack order, you get one PR, and
the goal's page says why. What landing came to stays on screen until you press
a key or move on.

## 7. Wait for it to land

The goal stays in the nav until it's **finished**. Every two minutes the
scheduler checks whether `origin/main` holds all of the goal's changes and
whether vex's checks on it have passed. A failing check keeps the goal in view
and shows the failing checks in red. When it finishes, the goal folds away
under ☑️ Finished, its worktrees are removed, and the goals waiting for it start.
Then `git pull` in vex. The `diatom/<goal>/…` branches are kept; delete them
whenever you like.

## Things to watch

- **Gate runs.** vex's `ci:check` runs every linter and the 100%-coverage tests,
  in each worktree, on every commit. Watch the first few gate runs in the
  scheduler's log before raising `maxSessions`.
- **Cost.** `[budget]` caps what sessions cost today, over the last 7 days
  and over the last 30. Once one is spent, the footer shows it in red and no
  new session starts; the ones running finish. Grilling runs on Opus at high
  effort, and implementation on Opus at medium. The window's title shows
  today's cost, and each goal's page its own and each task's. A session's
  cost counts on the days its agent worked, shared by how many steps it took
  on each, so a session that runs past midnight or resumes the next morning
  counts on both days. A finished goal's sessions count until they are 30
  days old. A spent day's budget only holds new sessions until midnight, so
  a goal bigger than a day's budget still finishes, over several days.
