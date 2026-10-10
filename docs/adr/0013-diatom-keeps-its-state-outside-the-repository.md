# 13. Diatom keeps its state outside the repository

Date: 2025-06-07

## Status

Accepted. It replaces the config walk-up and the in-repo `.diatom/`
directory of [ADR 0007](0007-one-window-per-repo-runs-its-scheduler.md).

## Context

Diatom kept everything it knew about a repository inside that repository, in
a `.diatom/` directory: the queue, the goals, the sessions, the worktrees and
a config file. The repository had to ignore it.

That made every repository diatom-aware. The worktrees sat under the
repository root, so every tool that walks the tree walked them too. Prettier,
linters, test runners and editors all saw the same files many times over. The
ignore rule had to go in a global gitignore, which is one more thing to carry
from machine to machine.

The config made the same demand. Diatom read a file in the repository, then
climbed the directories above it, then read the one in the home directory.
Where a value came from was hard to say and hard to change.

## Decision

**Diatom keeps no state in a repository.** There is no `.diatom/` directory.
The only file diatom still reads out of a repository is `.mcp.json`, which is
not diatom's: it is the repository's own list of MCP servers, and other tools
read it too.

**The state lives in one place in the home directory**, at
`$XDG_DATA_HOME/diatom`, which falls back to `~/.local/share/diatom`. The
ledger lives there too. `DIATOM_STATE` overrides it.

**A repository is named by its origin**, lowercased, without a scheme and
without a `.git`: `github.com/goodship-io/web`. The name is the path under
the state directory, which mirrors the way the repositories are checked out.
A repository with no origin has no name, so diatom leaves it out and says so
in the window rather than closing on the rest of the workspace.

**Each state directory holds a marker file** that names the repository it is
for, by absolute path. Diatom reads the markers to find two things:

- a repository whose origin changed, where exactly one marker still points at
  the path on disk. Diatom renames the directory and carries on.
- a state directory whose repository is gone. Diatom files a problem
  ([ADR 0014](0014-problems-are-what-diatom-cannot-get-past.md)) that asks
  for a new path or for the worktree to be removed.

**Two checkouts of one origin are refused.** The second one is left out of
the window, because both would claim the same state.

### The config

**There is one config file and the walk-up is gone.** A repository's settings
come from a `[repos."<prefix>"]` block in that file, where the prefix is the
start of the repository's name:

```toml
[repos."github.com/goodship-io"]       # every repo of the organisation
[repos."github.com/goodship-io/web"]   # that one repo
```

The longest prefix that covers the repository wins. The top of the file is
what every repository gets, and diatom's own defaults are below that.

`profiles` and `autoUpdate` are the human's, not a repository's. Diatom
refuses them inside a block.

**Diatom writes the file.** It explains every lever in generated comments, so
the comments always match the diatom reading them. Diatom writes the file the
first time it runs, with everything commented out. A key diatom does not know
is kept, at the end of its block, marked as doing nothing.

**The window sets the scalars.** A repository's heading in the nav opens a
page that lists every scalar lever, what it is now, and whether the
repository set it or took it from the level above. The page writes only the
keys that change. A map or a list is the file's, and the page opens the file.

### The migration

Diatom migrates a repository the first time it opens it: it moves `.diatom/`
to the new place with one rename, then repairs every worktree with one
`git worktree repair`. It refuses to migrate while the scheduler lock is held
or while any session is unsettled, because a move under a running session
loses work. The old directory is removed, not kept.

The migration is a one-time mechanism. Remove it, and the global gitignore
rule, once every repository has moved.

## Consequences

A repository is no longer diatom-aware. Nothing in it says diatom ever ran,
so no tool in it has to be told to look away.

Where a setting comes from is one file and a prefix, which is easy to read
and easy to write.

A repository with no origin cannot be worked on. That is the cost of naming
by origin, and the window says so rather than failing quietly.

Moving a checkout breaks nothing: the marker finds it again. Renaming it on
the forge breaks nothing either, as long as one marker still points at it.
