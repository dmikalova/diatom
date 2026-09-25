# 8. Review uses a native reviewer, not hunk

This decision records why diatom has its own diff reviewer rather than extending an existing one.

## Context

modem-dev/hunk is an excellent terminal diff viewer and supports line comments. However:

- It has no approve, reject or defer.
- Comments exist only in the memory of the running process, and other programs read them through a daemon CLI.
- Its extension API is experimental and "may change in breaking ways between minor releases", while releases come out every few days.
- Extensions are written in TypeScript and run inside hunk's Bun/Node process.

Go alternatives cover only part of the job:

- **umputun/revdiff** has line annotations but no review decisions.
- **tiffer** matches the approve/reject/defer model but has no license.

The review record (ADR 0001) is central to diatom, so it can't sit on an unstable API written in another language.

## Decision

**diatom ships its own reviewer (`diatom review`), built with Bubble Tea v2, go-gitdiff and chroma.**

The first version includes:

- a queue of hunks still to review
- a unified (stacked) diff view with syntax highlighting and **word-level diff**
- a line cursor with a comment editor
- keys to approve, reject, defer and step back
- atomic writes to `.diatom/`

A side-by-side view is not planned. Rendering code can be borrowed from MIT-licensed projects (revdiff, opencode's `internal/diff`). hunk is still useful for ad-hoc browsing. If its extension API becomes stable, this decision can be reconsidered.
