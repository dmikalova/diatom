# 7. One headless scheduler per machine, with panes as clients

This decision records how diatom's processes are laid out and how its configuration is found.

## Context

Goals can span repositories. For example, one goal runs in vex while the human notices something to do in dotfiles. Several goals may be running while others are parked for days. An overnight run has to survive the terminal closing. A Bubble Tea app can't embed another interactive TUI side by side with its own panes, because both need control of the terminal. Ghostty splits don't survive closing the window.

## Decision

**One headless scheduler (`diatom run`) per machine owns all queues, git operations and agent sessions. Every pane is a separate client that reads and writes `.diatom/` state.**

- **The scheduler picks the highest-priority ready task across the active goals of every known repo.** Goals can be pinned to be picked first. Each repo sets its own maximum number of sessions (e.g. 1 for vex, 3 for a work repo), and an optional machine-wide cap limits the total.
- **Whether a goal is active or parked is saved in the goal itself**, so it survives restarts. New goals start in planning.
- **`diatom workspace` opens a zellij layout**, which you can detach from and reattach to, with four panes:
  - **review**: the native reviewer (ADR 0008)
  - **status**: every goal in every repo, the active sessions and their output
  - **questions**: open questions from every goal
  - **intake**: free text for new work (ADR 0009)

  The scheduler runs in a second tab of the same session, so it survives the terminal closing with the panes. It takes a machine-wide lock, so a second `diatom run` refuses to start.

  Review and intake follow a single focused goal, kept in `~/.local/state/diatom/focus.yaml`, which the status pane writes. From the status pane, the human switches focus, opens another repo from a picker over the search roots, and parks or resumes goals.
- **Config is merged by walking up from the repository root** through its parent directories to `~/.config/diatom/config.yaml`, where the search stops. The closest file wins. This gives each directory tree its own defaults, such as a personal standard under `~/Code/github.com/dmikalova` and a work standard under `~/Code/github.com/goodship-io`, without adding files to work repos. Home-only settings (profiles to models, the machine-wide cap, the search roots) live in the XDG file.
