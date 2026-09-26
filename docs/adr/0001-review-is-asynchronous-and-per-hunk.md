# 1. Review is asynchronous and decided per hunk

This decision records the core interaction of diatom: agents keep working while the human reviews what they have already committed, and every correction flows back in as queued work.

## Context

Synchronous review tools (Cline, Aider, pi-hunk) pause the agent until the human looks at each change. For a large batch of similar work, such as implementing a 200-card KeyForge set where most cards come out right, that makes the human the bottleneck. The agent should keep going while the human approves most of the work and leaves comments on the rest.

## Decision

**The agent never waits for review.** It commits work, and the human reviews those commits later, at their own pace.

- **A review decision is made on one hunk of one commit:** approve, reject or defer. A commit is fully reviewed once every hunk in it has a decision, so "what is left to review" is a simple query.
- **A decision stays attached to the commit it was made on.** A later commit that rewrites the same code does not reopen it. That later commit is reviewed on its own terms.
- **Rejecting creates work immediately.** The rejected hunk's comments, with its lines as context, are added to the pending revision of its commit. There is no separate "submit review" step.
  - **There is one revision per revised commit**, so each lands as that commit's fixup (ADR 0003). A workstream's pending revisions still run together in one batch (ADR 0004).
  - **The reviewer only records decisions.** The scheduler turns them into revision tasks on its next pass, within seconds, because only the harness writes task files (ADR 0002).
- **Defer means "not now" for the human only.** A deferred hunk stays in the review queue behind unreviewed hunks and creates no agent work. The human is warned about deferred hunks before the goal is finished.
- **A comment on an approved hunk becomes an intake** (ADR 0009), not a revision.
- **Decisions can be changed.** The reviewer keeps a history the human can step back through, to fix a hunk approved on autopilot.
  - Approve → reject creates or extends a revision.
  - Reject → approve before the scheduler picks the revision up removes that hunk's comments from it.
  - Reject → approve after pickup is recorded, but the revision is already running. Its fixup commit comes back for review like any other commit.
- **A merge is reviewed by what its committer changed from the automatic merge** (git's remerge diff): a conflict resolution, or the repair of a merge that failed the gate. A clean merge has nothing to review.
- **Reviewing a revision** shows the comments that caused it above the fixup's diff. A toggle switches to the combined view: the original hunk with the fix folded in, as it will look after autosquash.
