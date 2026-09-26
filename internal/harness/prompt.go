package harness

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/schedule"
)

// PromptInput is what a batch's prompt is built from.
type PromptInput struct {
	Goal  *queue.Goal
	Batch schedule.Batch
	Gate  string
	// TaskDir holds the batch's task files while it runs.
	TaskDir string
	// Merging is set when a merge is in progress in the worktree.
	Merging bool
	// Guides are the AGENTS.md files below the worktree's root.
	Guides []string
}

// Prompt builds a batch's instructions. ADR 0006 has prompts refer to files
// rather than paste them, and so the repo's code and agent instructions are
// left for the agent to read. A task's own body is pasted, though: it is the
// instruction itself, such as a revision's review comments, and in practice
// agents given only the path and title worked from the title alone.
func Prompt(in PromptInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are working through %s in the %q workstream of the goal %q. "+
		"Your working directory is the workstream's own git worktree.\n\n",
		plural(len(in.Batch.Tasks), "task"), in.Batch.Workstream, in.Goal.Title)

	b.WriteString("## How diatom works\n\n")
	b.WriteString(
		"- **Only edit files.** diatom does every git operation, and git commands that change the " +
			"repository are blocked. Read-only ones such as `git status`, `git diff` and `git log` are fine.\n",
	)
	fmt.Fprintf(
		&b,
		"- **The gate runs when you finish.** diatom runs `%s` before the session may end. "+
			"If it fails you get its output back and keep fixing. When it passes, diatom commits your work.\n",
		in.Gate,
	)
	b.WriteString("- **Report each task with the task tool**, a shell command:\n")
	b.WriteString("  - `diatom task done <id>` once the task is finished.\n")
	b.WriteString(
		"  - `diatom task note <id> \"<text>\"` to record something a later task or the reviewer " +
			"should know.\n",
	)
	b.WriteString(
		"  - `diatom task ask <id> \"<question>\"` when the task needs a decision from the human. " +
			"The task is parked until they answer; don't guess, and move on to the other tasks. Leave the files " +
			"passing the gate.\n",
	)
	b.WriteString(
		"- A task you don't mark done goes back in the queue, and a later session continues from " +
			"the files you leave.\n",
	)
	b.WriteString(
		"- The task files are read-only for you: add to them only through the task tool.\n\n",
	)

	if len(in.Guides) > 0 {
		b.WriteString(
			"## Directory instructions\n\nBefore working in one of these directories, read its " +
				"AGENTS.md; it applies to everything below it.\n\n",
		)
		for _, g := range in.Guides {
			fmt.Fprintf(&b, "- `%s`\n", g)
		}
		b.WriteString("\n")
	}

	switch in.Batch.Kind {
	case queue.Revision:
		b.WriteString(
			"## These are revisions\n\nThe human reviewed commits and rejected some hunks. Each task " +
				"below carries the rejected hunks of one commit with the comments on them. Work the revisions one " +
				"at a time, and run `diatom task done <id>` as soon as each is finished, before starting the next: " +
				"diatom splits your work at those points and commits each revision as a fixup of the commit it " +
				"revises. A comment may ask for no change once you look into it; say why with `diatom task note` " +
				"and mark the task done.\n\n",
		)
	case queue.Conflict:
		b.WriteString(
			"## A merge is in progress\n\nThe integration branch is being merged into this workstream " +
				"and some files conflict. Resolve every conflict (`git status` and `git diff` show them), keeping " +
				"the intent of both sides, and remove every conflict marker. Don't commit: diatom completes the " +
				"merge once the gate passes.\n\n",
		)
	case queue.GateRepair:
		if in.Merging {
			b.WriteString(
				"## A merge is in progress\n\nThe integration branch merged into this workstream " +
					"cleanly, but the result fails the gate. Make the gate pass without undoing either side's " +
					"work. Don't commit: diatom completes the merge.\n\n",
			)
		} else {
			b.WriteString(
				"## The gate is failing\n\nThis workstream fails its gate. Make it pass.\n\n",
			)
		}
	}

	b.WriteString(
		"## Tasks\n\nWork through them in order. Each task's file is shown for reference; " +
			"its text is included below.\n",
	)
	for _, t := range in.Batch.Tasks {
		fmt.Fprintf(
			&b,
			"\n### Task %s: %s\n\n`%s`\n\n",
			t.ID,
			t.Title,
			filepath.Join(in.TaskDir, t.ID+".md"),
		)
		if body := strings.TrimSpace(t.Body); body != "" {
			b.WriteString(body + "\n")
		}
	}
	b.WriteString("\nWhen every task is done or asked, end the session.\n")
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
