package harness

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dmikalova/diatom/internal/plan"
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
		"- **Nobody reads your replies.** The session runs unattended: the human sees only what you " +
			"report with the task tool below. A question or result left in a reply is lost.\n",
	)
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

// planningPrompt builds the instructions of a triage or grilling session.
func (h *Harness) planningPrompt(repo Repo, g *queue.Goal, in PromptInput) (string, error) {
	tasks, err := repo.Store.Tasks(g.Name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"You are %s the goal %q. Your working directory is a read-only checkout of the goal's "+
			"integration branch: read the code freely, but anything you write in it is thrown away.\n\n",
		map[queue.Kind]string{queue.Triage: "triaging new input for", queue.Grilling: "grilling"}[in.Batch.Kind],
		g.Title,
	)
	b.WriteString("## How diatom works\n\n")
	b.WriteString(
		"- **Nobody reads your replies.** The session runs unattended: the human sees only what you " +
			"report with the task tool below. A question or result left in a reply is lost.\n",
	)
	b.WriteString(
		"- You don't change code in this session. Report through the task tool, a shell command:\n",
	)
	b.WriteString(
		"  - `diatom task ask <id> \"<question>\"` asks the human something. The task waits for the " +
			"answer, which comes back in the task text.\n",
	)
	b.WriteString("  - `diatom task note <id> \"<text>\"` records something worth keeping.\n")
	b.WriteString("  - `diatom task done <id>` marks the task done.\n")
	if in.Batch.Kind == queue.Triage {
		writeTriage(&b, g, tasks)
	} else {
		writeGrilling(&b, repo, in)
	}
	if len(in.Guides) > 0 {
		b.WriteString("\n## Directory instructions\n\n")
		for _, gd := range in.Guides {
			fmt.Fprintf(&b, "- `%s`\n", gd)
		}
	}
	b.WriteString("\n## Tasks\n")
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
	return b.String(), nil
}

func writeTriage(b *strings.Builder, g *queue.Goal, tasks []*queue.Task) {
	b.WriteString(
		"  - `diatom task add-task <id> -ws <workstream> -title \"<title>\" [-after <ids>] < body` " +
			"adds a task to one of the goal's workstreams, with its details on stdin.\n",
	)
	b.WriteString(
		"  - `diatom task new-goal <id> -title \"<title>\" < description` starts a new goal, for " +
			"input that doesn't belong to this one.\n\n",
	)
	b.WriteString(
		"## Triage\n\nEach task below is one intake: free-form input from the human, such as a new " +
			"goal or a handful of playtest notes. Sort it into small, clear-cut tasks on the goal's workstreams, a " +
			"new goal, or questions back to the human when something is unclear. Anything that needs a design " +
			"decision is a question, not a task. You can't add a workstream: ask instead. Mark each intake done " +
			"once it is sorted.\n\n",
	)
	b.WriteString("The goal's workstreams:\n\n")
	for _, w := range g.Workstreams {
		deps := ""
		if len(w.DependsOn) > 0 {
			deps = " (after " + strings.Join(w.DependsOn, ", ") + ")"
		}
		fmt.Fprintf(b, "- %s%s\n", w.Name, deps)
	}
	b.WriteString("\nIts tasks, for placing new work and naming what it comes after:\n\n")
	for _, t := range tasks {
		if t.Kind == queue.Triage || t.Kind == queue.Grilling {
			continue
		}
		fmt.Fprintf(b, "- %s [%s, %s] %s\n", t.ID, t.Workstream, t.State, t.Title)
	}
}

func writeGrilling(b *strings.Builder, repo Repo, in PromptInput) {
	b.WriteString(
		"  - `diatom task plan <id> < plan.yaml` hands in the plan. It is checked straight away; " +
			"fix what it reports and hand it in again.\n\n",
	)
	b.WriteString(
		"## Grilling\n\nThe goal below hides decisions you can't make alone: what is new, how it " +
			"fits the code, what order the work goes in. Find them before any work starts. If you have the " +
			"`diatom:grilling` skill, use its method to find them, but put every question to the human with " +
			"`diatom task ask`, one question per call with your recommended answer in it, never in a reply. " +
			"Grilling works in rounds, one per session:\n\n",
	)
	b.WriteString(
		"- **Ask a round.** Put every question you need answered now with `diatom task ask`, then " +
			"end the session. The answers come back in the task text, and the next round starts from them.\n",
	)
	b.WriteString(
		"- **Or hand in the plan** once nothing is left to decide, then mark the task done. The human " +
			"signs it off before work starts.\n\n",
	)
	b.WriteString(
		"The plan is YAML: workstreams, scoped by purpose rather than path, with the order between " +
			"them, and small tasks, each one session's work for an agent.\n\n",
	)
	b.WriteString(
		"```yaml\nsummary: What the goal does and how, in a few sentences.\nworkstreams:\n" +
			"  - name: engine\n  - name: cards\n    dependsOn: [engine]\ntasks:\n  - key: ward\n" +
			"    title: Add the ward keyword\n    workstream: engine\n    body: |\n      What to do and how to " +
			"know it's done.\n  - key: warden\n    title: Implement Warden\n    workstream: cards\n" +
			"    after: [ward]\n    profile: implementation\n```\n\n",
	)
	names := make([]string, 0, len(repo.Config.Profiles))
	for n := range repo.Config.Profiles {
		names = append(names, n)
	}
	slices.Sort(names)
	fmt.Fprintf(b, "A task's profile is optional and one of: %s.\n\n", strings.Join(names, ", "))
	drafts := plan.DraftsDir(repo.Store.GoalDir(in.Goal.Name))
	if dir := repo.Config.ADR.Dir; dir != "" {
		fmt.Fprintf(
			b,
			"Where a decision warrants an ADR, write it as a Markdown file in `%s`, named and "+
				"numbered to follow the repo's ADRs in `%s`. They are committed there when the plan is signed "+
				"off, and reviewed like code.",
			drafts,
			dir,
		)
	} else {
		fmt.Fprintf(
			b,
			"Where a decision warrants an ADR, write it as a Markdown file in `%s`; it stays with "+
				"the goal.",
			drafts,
		)
	}
	if f := repo.Config.ADR.Format; f != "" {
		b.WriteString(" Write them this way: " + f)
	}
	b.WriteString("\n")
}
