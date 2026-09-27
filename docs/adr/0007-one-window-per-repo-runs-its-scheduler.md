# 7. One window per repo, with its scheduler inside

This decision records how diatom's process and window are laid out, and how its configuration is found.

## Context

Several goals may run in a repo at once, while others are parked for days.

diatom first ran one scheduler per machine across every repo it knew about, with a repo picker, a machine-wide focus and a machine-wide session cap. Working in two repos at once rarely needs them to share a queue, and one diatom per repo is simpler to run and to reason about: everything it touches is in the repo it was started in.

It then ran a headless scheduler per repo beside a zellij session of four panes: review, status, questions and intake. The panes showed everything at once, each its whole slice, rather than the right thing at the right time. Answering questions was hardest: the questions pane had no room to say what a goal was for, and the context changed with every answer. zellij bought two things. Detaching kept the agents running with nobody watching, which isn't wanted. Text could be selected inside one pane.

A single Bubble Tea program can hold several parts that each take the keyboard, like a web page's navigation beside its content, and handle the mouse itself.

## Decision

**diatom works in one repo: the one it is started in. `diatom` opens one window on it, and the window runs the repo's scheduler in the same process. Closing the window suspends the sessions (ADR 0012), so nothing runs while diatom is closed.**

- **Every command finds its repo from the directory it runs in**, anywhere in the repo, a goal's worktree included. Outside a git repo, and in a repo that doesn't ignore `.diatom/`, diatom refuses to run. Working in two repos means running two diatoms.
- **The scheduler picks the highest-priority ready task across the repo's active goals.** The repo sets its maximum number of sessions. It takes the repo's lock, `.diatom/run.lock`. A second window on the same repo runs no scheduler and only views, though answering and reviewing still work there, since everything the window changes is a file in `.diatom/`. The scheduler logs to `.diatom/diatom.log`.
- **Whether a goal is active or parked is saved in the goal itself**, so it survives restarts. New goals start in planning.
- **The window is a nav down the left and a main pane showing what the nav selects.**
  - The nav lists Next, the intake, and the goals being worked on, each on two lines: a glyph for where it stands and its title, then the one thing about it that matters most now, what waits on the human first. Next's second line counts what waits, then the sessions running. A menu at its foot holds the finished goals, the latest first, and the scheduler's log. Its footer shows what the work has cost today, over the last 7 days and over the last 30, red where that spends the budget (ADR 0004), and the scheduler's health, and opens the scheduler's log. The intake box sits at its foot.
  - **Next** is everything waiting on the human, one item at a time: plans to sign off, then questions, then goals ready to finish, then hunks to review, each level in the nav's order of goals. A plan holds a whole goal up and a question a task, finishing a goal lets the goals waiting for it start, and a review holds up nothing. The hunks finishing a goal brought, catching up with its base or settling a rebase's conflicts, hold up its landing, so they come with the goals ready to finish. Answering moves on to the first item left, and nothing else changes the item shown.
  - An item of Next has three parts: what its goal is for, which opens the goal's page; the item itself; and the answer, or what can be done.
  - A goal's page says where it stands and what it is for, then offers its actions and its review, its plan, and its tasks, each opening to its sessions and their steps.
  - Tab moves through the nav, the main pane's parts and the intake box. Clicking selects and focuses, the wheel scrolls, and dragging the nav's edge resizes it.
  - An intake carries what the main pane showed when it was sent, as a clue for triage (ADR 0009).
  - With the terminal in the background, a notification says when Next has something again after having nothing.
- **Config is merged by walking up from the repository root** through its parent directories to `~/.config/diatom/config.toml`, where the search stops. The closest file wins. This gives each directory tree its own defaults, such as a personal standard under `~/Code/github.com/dmikalova` and a work standard under `~/Code/github.com/goodship-io`, without adding files to work repos. Home-only settings (profiles to models, and updating diatom itself) live in the XDG file.

The cost is that the window holds the mouse: the terminal's own selection needs its modifier key and runs across the whole window. Copying the part that has the keyboard, with `y` or cmd+c, stands in for it.
