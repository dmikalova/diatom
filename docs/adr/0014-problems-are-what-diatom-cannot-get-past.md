# 14. Problems are what diatom cannot get past

Date: 2025-06-07

## Status

Accepted. It extends
[ADR 0009](0009-intake-is-triaged-and-questions-park-tasks.md), which parks a
task on a question. A problem parks a goal the same way.

## Context

Diatom does work in the background that can fail in ways no agent can fix: a
commit that git refuses, a push that the forge rejects, a pull request that
somebody closed, a worktree whose repository is gone.

These failures used to reach the human as an error in the window's footer.
The footer is a passing thing. The next key cleared it, and the error turned
up again later under an unrelated item, where it meant nothing. The goal kept
running, so the failure happened again, and again.

## Decision

**A failure diatom cannot get past is a problem**, and a problem is a file,
like everything else in the queue: `problems/open/<id>.md` under the goal,
moved to `problems/done/` when it is dealt with.

**An open problem parks its goal.** Nothing of that goal runs until the
problem is closed, exactly as a question parks its task. The goal shows as
stuck in the nav.

**A problem leads Next.** It comes before the plans, the questions and the
reviews, because diatom itself is stopped and nothing else of that goal
matters until it moves.

**The line is where the failure ran.** Anything that fails in the background
becomes a problem. Anything that fails while the human waits on a key stays
an error in the footer, because they are there to read it.

**Problems are keyed by goal and operation.** The same failure again replaces
the old problem and counts up, rather than piling up a list of the same
thing. A problem about a repository, not a goal, goes on that repository's
hidden intake goal.

**Every problem offers three actions**: retry, give up on the goal, or reply.
A reply becomes a task on the goal, with the failed output in its body and
the reply as the instruction, and closes the problem, which unparks the goal.

**A problem's kind adds its own actions** to those three. The actions are
what diatom can carry out itself, so a kind that adds none is one only the
human can put right.

| Kind         | What it is                                    | What it adds                             |
| ------------ | --------------------------------------------- | ---------------------------------------- |
| `commit`     | a batch of a goal failed                      | nothing                                  |
| `land`       | a goal cannot take in what its base gained    | nothing                                  |
| `pr`         | reading a goal's pull requests keeps failing  | nothing                                  |
| `pr-closed`  | a pull request was closed, not merged         | paste the new URL, or open another PR    |
| `worktree`   | the repo a queue is kept for is gone          | point at a new path, or forget its goals |
| `repo`       | a repo has no origin to name its state by     | nothing                                  |
| `checkout`   | a second checkout of a repo diatom works in   | nothing                                  |
| `config-key` | the config sets a key diatom has no lever for | delete the key from the config           |

**One failed call to the forge is not a problem.** Diatom reads a done goal's
pull requests on every pass, and the GitHub CLI fails calls for reasons that
pass on their own, such as a moment without network. Only a run of failures
is raised, and the problem closes itself once they read again.

**Replies are taken in for every goal, before anything is watched.** A done
goal is never scheduled, so taking replies in while the goals run would never
reach one; and watching a goal again before its reply is read would file the
same failure on top of the answer and spend it.

**The file keeps the output whole.** Next shows the last 30 lines, which is
enough to say what went wrong. `o` opens the whole file in the editor.

## Consequences

A failure stays until somebody deals with it, and it stays where it belongs:
on the goal it happened to, not under whatever is on screen.

A goal that diatom cannot get past stops costing money, because nothing of it
runs.

Diatom needs a kind for every failure it means to raise this way. A failure
with no kind still gets the three universal actions, so nothing is lost by
raising one before its kind is written.
