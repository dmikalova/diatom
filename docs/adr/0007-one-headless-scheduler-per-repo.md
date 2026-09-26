# 7. One headless scheduler per repo, with panes as clients

This decision records how diatom's processes are laid out and how its configuration is found.

## Context

Several goals may run in a repo while others are parked for days, and an overnight run has to survive the terminal closing. A Bubble Tea app can't embed another interactive TUI side by side with its own panes, because both need control of the terminal. Ghostty splits don't survive closing the window.

diatom first ran one scheduler per machine across every repo it knew about, with a repo picker, a machine-wide focus and a machine-wide session cap. Working in two repos at once rarely needs them to share a queue, and one diatom per repo is simpler to run and to reason about: everything it touches is in the repo it was started in.

## Decision

**diatom works in one repo: the one it is started in. Each repo has one headless scheduler (`diatom run`), which owns the repo's queue, git operations and agent sessions. Every pane is a separate client that reads and writes the repo's `.diatom/` state.**

- **Every command finds its repo from the directory it runs in**, anywhere in the repo, a goal's worktree included. Outside a git repo, and in a repo that doesn't ignore `.diatom/`, diatom refuses to run. Working in two repos means running two diatoms.
- **The scheduler picks the highest-priority ready task across the repo's active goals.** Goals can be pinned to be picked first. The repo sets its maximum number of sessions. It takes the repo's lock, `.diatom/run.lock`, so a second `diatom run` in the same repo refuses to start.
- **Whether a goal is active or parked is saved in the goal itself**, so it survives restarts. New goals start in planning.
- **`diatom workspace` opens the repo's zellij session**, named for the repo, which you can detach from and reattach to, with four panes:
  - **review**: the native reviewer (ADR 0008)
  - **status**: every goal in the repo, what intake is being sorted, the active sessions and their output
  - **questions**: open questions from every goal and from triage
  - **intake**: free text for anything the human wants done (ADR 0009)

  The scheduler runs in a second tab of the same session, so it survives the terminal closing with the panes.

  Review and intake follow the goal the human is looking at, kept in `.diatom/focus.yaml`, which the status pane writes. From the status pane, the human switches focus, focuses the repo itself, and parks or resumes goals.
- **Config is merged by walking up from the repository root** through its parent directories to `~/.config/diatom/config.yaml`, where the search stops. The closest file wins. This gives each directory tree its own defaults, such as a personal standard under `~/Code/github.com/dmikalova` and a work standard under `~/Code/github.com/goodship-io`, without adding files to work repos. Home-only settings (profiles to models, and updating diatom itself) live in the XDG file.
