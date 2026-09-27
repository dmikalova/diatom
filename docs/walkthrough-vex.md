# Running diatom on vex

This walks through running diatom on vex: turning `docs/todo-agent.md` into
goals, working through them, and landing them. You run every step yourself. It
has four phases: set vex up, turn the todo-agent into goals, work through them,
and land them.

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
  and `mage ci:check` as the gate of every Go repo.
  From here on the scheduler installs each new release itself and restarts on
  it, resuming its sessions.
- Your global git ignore already lists `.diatom/`, which diatom requires before
  it creates a goal.

## 1. Prepare vex

1. **Commit what the agents should see.** Agents work from committed `main`.
   Your uncommitted `docs/todo.md` change is invisible to them, which is fine
   since agents never read that file. But anything they should see has to be
   committed first, including the project-standards v1.32.0 bump in `go.mod`
   and `go.sum`.
2. **Optionally create `.diatom/config.toml`.** It's ignored, so it stays
   local:

   ```toml
   maxSessions = 2   # agent sessions at once in vex; raise it once you trust it
   ```

   vex needs no gate of its own: it has a `go.mod`, so it gets the Go gate
   from your home config, `mage ci:check`. Without one, `diatom workspace`
   would ask for it. The gate is `ci:check`, not `ci:fix`, because it must
   only check. vex's AGENTS.md forbids git operations, which matches diatom:
   the harness makes every commit.

   vex's `ci:check` takes about 12 seconds warm, and over a minute on a cold
   build cache. A gate still running after 2 minutes is stuck: diatom stops
   it and counts it as failed. Each command an agent runs is stopped after 30
   seconds, so agents run narrow checks and leave the whole gate to diatom.
   Set `gateTimeout` or `commandTimeout` in this file to change either.

## 2. Open the workspace

```sh
cd ~/Code/github.com/dmikalova/vex && diatom workspace
```

- **Work tab:**
  - **Reviewer** on the left, 60% wide.
  - **Status** lists the intake triage is sorting, then every goal; its title
    shows the repo and the focus. Every menu opens its selection with
    `enter` or `space`, and `esc` backs out one level:
    - A goal opens to a menu of what can be done with it now (sign off, mark
      done, open PRs, push, park, pin), then its tasks, each running one with
      its latest step. Each menu entry shows its key, which also works
      straight from the list.
    - A task opens to the Claude sessions that worked on it, newest first:
      running, or how each ended, its cost, and why diatom couldn't commit
      its work if it couldn't.
    - A session opens to its steps, one line each in Claude's own words, and
      the gate's runs. It follows the latest step while the session runs.
    - A step opens to its full command and the end of its output.
    - At the top, `esc` focuses the repo itself, and `f` focuses a goal
      without opening it.
  - **Questions** is where you answer the agents and sign off plans.
  - **Intake** is where you type new work or notes.
- **Scheduler tab:** `diatom run` and its log. It starts sessions as soon as
  work is ready.

Stopping never loses work. Ctrl-C in the scheduler tab, or `diatom stop` from
anywhere, suspends every session within seconds with its files as they are.
The next `diatom run` resumes each agent's own Claude session where it
stopped. `diatom stop -drain` lets the running sessions finish instead.

## 3. Turn the todo-agent into goals

Break it up by what lands together. Areas that edit the same code belong in one
goal, because conflicts inside a goal are fixed as the work goes. Conflicts
between goals only surface when the second one lands. Each goal's workstreams
become one stacked PR each. Four goals:

| Goal                  | Section                                           | Why on its own                                                     |
| --------------------- | ------------------------------------------------- | ------------------------------------------------------------------ |
| `effect-catalog`      | Closed effect catalog + `RulesBearing` (ADR 0018) | Already decided; rulebook and tests                                |
| `forgekey-purge`      | `ForgeKey` bakes "purge self"                     | Already decided; small                                             |
| `effect-glyphs`       | gocognit: the `effectGlyphs` exclusion            | The doc itself asks for a glyph-family grilling                    |
| `mass-mutation-sweep` | Post-Mass-Mutation cleanup sweep, all subsections | Large and heavily overlapping; grilling splits it into workstreams |

With the repo focused (`esc` in status), type this into the intake pane and
press `enter` to send it. `shift+enter` starts a new line:

```text
Turn docs/todo-agent.md into goals, one per ## section. The ForgeKey and effect
catalog sections are already decided. The Post-Mass-Mutation cleanup sweep
waits for the effect catalog and ForgeKey goals. Ignore the file's preamble:
it's for agents in chat, not for you.
```

Triage reads the file and starts the four goals. It hands in the ForgeKey plan
with its goal, so that one skips grilling and waits for your sign-off; the
effect catalog may too, if the section decides every task. The effect glyphs
goal is grilled. The status pane shows the intake as "being sorted" until
triage is done: open it with `enter` to watch triage work. Anything triage
can't decide comes to the questions pane, filed under intake.

The sweep waits: nothing of it runs, grilling included, until the effect
catalog and ForgeKey goals are finished (step 7), and status shows it "waiting
for" them. Its branch then starts from `main` with both landed, pulled or not,
so it never overlaps them. Set or change what a goal waits for from the command
line too: `diatom goal after mass-mutation-sweep effect-catalog forgekey-purge`.

The command line works too. `section` prints one `##` section of the file, and
`diatom goal new` starts a goal from it without triage:

```sh
cd ~/Code/github.com/dmikalova/vex
section() { awk -v h="$1" '/^## /{p = index($0, h) > 0} p' docs/todo-agent.md; }
section 'gocognit gate' | diatom goal new effect-glyphs -title "Split effectGlyphs by glyph family"
```

## 4. Grilling: answer questions, then sign off each plan

Each planning goal gets grilling rounds. These are read-only planning sessions
on Opus that ask questions and then hand in a plan.

1. **Answer questions** in the questions pane: `j`/`k` to move, `enter` to
   open a question with its full text, `enter` again to send your answer and
   move on to the next, and `esc` to go back. Each question comes with its
   context. The next round starts only after every question in the current
   round is answered.
2. **Review the plan.** A plan ready for sign-off goes to the top of the
   questions pane, and answering a question takes you straight to a plan you
   haven't seen yet. It shows the workstreams and tasks in order (or run
   `diatom goal plan <goal>`).
3. **Correct it or approve it** from the plan's answer box.
   - To correct it, write what to change and press `enter`. The plan is set
     aside and a new round starts with your feedback.
   - To approve it, press `enter` twice with nothing typed (or `s` twice on the
     goal in status, or run `diatom goal approve <goal>`). This queues the
     tasks and commits any ADR drafts from grilling, which then come up in
     the reviewer like any other commit.

## 5. While the work runs

In status, each goal shows its task counts, open questions, hunks waiting for
review, its cost so far, and a `▶ <workstream>` line with the latest step of
any running session. An active goal reads **queued** while its ready work
waits for a free session, **reviewing** once every task is done and hunks are
left, and **ready to finish** once those are reviewed too.

A freed session goes to the most urgent kind of work first: fixes, then
revisions, triage, grilling and planned work. Among work of the same kind it
goes to the goal highest in the list. Pinned goals come first, and `P` pins
one.

What happens without you:

- Every commit that passes the gate is merged into the goal's integration
  branch right away, and downstream workstreams pick it up before their next
  task.
- A commit that fails the gate is sent back to the agent. If it keeps failing,
  it's retried once with more thinking, then becomes a question to you.
- Merge conflicts between workstreams become conflict-resolution tasks.

What needs you:

- **Questions** from implementation agents show up in the same pane. Answering
  one unblocks its task.
- **Review, whenever you like.** The reviewer follows the focused goal:
  - `a`, `r` and `d` approve, reject and defer the hunk on screen.
  - `c` comments on the line under the cursor, and `u` steps back through
    earlier decisions.
  - Rejecting with a comment becomes a revision task within seconds. The fix
    comes back as a `fixup!` commit, and `v` shows it folded into the original.
  - Review doesn't hold up the agents, but you have to review everything before
    step 6.
  - Hunks in files matching `autoApprove` in `.diatom/config.toml`, which is
    `*_test.go` for vex, are approved for you and never reach the reviewer. Its
    header counts them.
- **New thoughts mid-goal:** focus the goal and type in intake. Triage turns it
  into tasks, a question, or a separate goal. A task on a workstream the goal
  doesn't have comes back to you as a question.
- **Controls:** `p` parks or resumes a goal. Nothing new starts while parked,
  and nothing is lost. `P` pins a goal so its tasks run before other goals'.

## 6. Mark a goal done

Once every task is done and every hunk reviewed, open the goal in status and
pick **Mark it done** with `enter` twice, or press `d` twice (or run
`diatom goal done <goal>`).

- diatom replays the goal's commits onto `main` without the merge commits, with
  each fixup squashed into its target. It runs the gate on each PR's tip and
  shows the stack.
- **Mark it done with work left**, `D` twice (or `-force`), marks it done
  even with work left.
- If the workstreams can't be put in stack order, you get one PR, and the
  status line says why.

## 7. Land it

Open the goal in status and choose one from its menu:

- **`U` twice pushes straight to `main`.** You've already reviewed every hunk
  inside diatom, so this is the simple path for a solo repo, and vex's CI/CD
  runs on the push.
- **`F` twice opens the stacked PRs**, one per workstream, each based on the one
  before. Use this when you want CI per PR, as for the big sweep. Merge them
  bottom-up using **"Create a merge commit"**. Squash or rebase merges rewrite
  the lower PR's commits, so the next PR up would show them all again.

The goal stays in status until it's **finished**. Every two minutes the
scheduler checks whether `origin/main` holds all of the goal's changes and
whether vex's checks on it have passed. A failing check keeps the goal visible
and shows the failing checks in red. When it finishes, the goal leaves the list
and its worktrees are removed. Then `git pull` in vex. The `diatom/<goal>/…`
branches are kept; delete them whenever you like.

Then start Phase 2: create `mass-mutation-sweep` and repeat steps 4–7.

## Things to watch on this first real run

- **Gate runs.** vex's `ci:check` runs every linter and the 100%-coverage tests,
  in each worktree, on every commit. Watch the first few gate runs in the
  scheduler log before raising `maxSessions`.
- **Cost.** diatom has no spending cap; `maxSessions` is the only limit.
  Grilling runs on Opus at high effort, and implementation on Opus at medium.
  Each task's usage is recorded in its file under `.diatom/goals/<goal>/tasks/`.
- **`docs/todo-agent.md` edits.** vex's AGENTS.md tells agents to delete
  finished items from this file, so each goal edits its own section of it. The
  goals touch different parts of the file, so they should land without
  conflicting.
