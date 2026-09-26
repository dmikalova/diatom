# 12. Stopping suspends sessions, and diatom updates itself

This decision records what happens to running sessions when the scheduler stops, and how diatom moves to a new release.

## Context

The scheduler has to stop now and then: to install a new diatom, to reboot, or to close the workspace. A session can run for many minutes, so waiting for every session to finish can take a long time. Killing the sessions threw their work away: the task started over in a new session that knew nothing of the old one, beside the old session's uncommitted edits. Asking the agent to write down where it was needs a live turn, which a headless session can't be sent. It would also be a lossy summary of something that already exists: Claude Code saves each session's whole transcript as it goes.

## Decision

**Stopping suspends each session, and the next scheduler resumes it where it stopped.**

- **A stop takes seconds.** An interrupt or SIGTERM to `diatom run`, or `diatom stop`, stops each agent within seconds: an interrupt first, then a terminate, and a kill at 15 seconds. The agent's tasks stay active, and its worktree keeps its files as they are.
- **Resuming is the agent's own session again.** The session records the agent's session ID the moment the agent starts. The next scheduler carries that session on in the same worktree with `claude --resume`, with a note saying it was stopped and that a command it was running was killed.
- **Where the stop lands decides what runs again.** A stop during the agent resumes the agent. A stop during the gate runs the gate again. The gate runs before anything the session reported is applied, so nothing is applied twice. A commit or merge under way always finishes first, so git is never left half done. A session whose agent never started, or that was resumed three times, starts over as a new session.
- **Draining is still there when wanted.** `diatom stop -drain` starts no new sessions and exits once the running ones finish. A second stop, more than two seconds after the first, exits at once.
- **Agents and gates run in their own process groups**, so the terminal's interrupt reaches the scheduler and not the agents, and stopping one stops everything it started. Agents run every command in the foreground, because a headless session ends as soon as the agent stops to wait for a background command.

**diatom updates itself by suspending and handing its process over.**

- **With `autoUpdate: true`**, the scheduler looks for a new release when it starts and hourly after that. It installs the release with `go install` while the sessions keep working, then suspends them and replaces itself with the new binary in the same process and pane. The new binary resumes the sessions.
- **Without it**, a new release is only logged.
- **A binary built from a checkout never updates itself**, because its builder is working on it. It is still followed: when the binary on disk is replaced, by an update or a new build, the scheduler suspends and restarts on it. Each pane restarts on it too, once nothing is being typed there.

The cost is that the part of a session before a stop is not billed in its usage, because the agent never reports it.
