package harness

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dmikalova/diatom/internal/plan"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/roster"
	"github.com/dmikalova/diatom/internal/schedule"
)

// PromptInput is what a batch's prompt is built from.
type PromptInput struct {
	Goal  *queue.Goal
	Batch schedule.Batch
	// Gate is the repo's gate, and Fix what diatom runs before it, if
	// anything.
	Gate, Fix string
	// Timeout is how long each command the agent runs may take.
	Timeout time.Duration
	// TaskDir holds the batch's task files while it runs.
	TaskDir string
	// Merging is set when a merge is in progress in the worktree.
	Merging bool
	// Guides are the AGENTS.md files below the worktree's root.
	Guides []string
	// Goals are the repo's goals, this one included.
	Goals []roster.Brief
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
	if in.Fix != "" {
		fmt.Fprintf(
			&b,
			"- **The gate runs when you finish.** diatom runs `%s` to format and regenerate "+
				"files, then the gate `%s`, before the session may end. Don't run either yourself: they take "+
				"minutes. If the gate fails you get its output back and keep fixing. When it passes, diatom "+
				"commits your work.\n",
			in.Fix,
			in.Gate,
		)
	} else {
		fmt.Fprintf(
			&b,
			"- **The gate runs when you finish.** diatom runs `%s` before the session may "+
				"end; don't run it yourself. If it fails you get its output back and keep fixing. When it passes, "+
				"diatom commits your work.\n",
			in.Gate,
		)
	}
	fmt.Fprintf(&b, "- **Commands run in the foreground, and each is stopped after %s.** "+
		"Backgrounding a command, with `&` or `nohup`, and sleeping to wait for one are refused. "+
		"Run the narrowest check that tells you what you need, such as one package's tests; diatom "+
		"runs the whole gate itself when you finish.\n", in.Timeout)
	b.WriteString(silentGuide)
	b.WriteString(readGuide)
	b.WriteString("- **Report each task with the task tool**, a shell command:\n")
	b.WriteString(
		"  - `diatom task done <id> \"<summary>\"` once the task is finished. The summary is all the " +
			"human reads about how the task went: two or three plain sentences on what changed and anything " +
			"they should know, such as a choice you made or something you left out.\n",
	)
	b.WriteString(
		"  - `diatom task note <id> \"<text>\"` to record something a later task or the reviewer " +
			"should know.\n",
	)
	b.WriteString(
		"  - `diatom task ask <id> \"<question>\"` when the task needs a decision from the human. " +
			"The task is parked until they answer; don't guess, and move on to the other tasks. Leave the files " +
			"passing the gate.\n",
	)
	b.WriteString(askGuide)
	b.WriteString(manualGuide)
	b.WriteString(newGoalGuide)
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

	writeOthers(&b, in)

	switch in.Batch.Kind {
	case queue.Revision:
		b.WriteString(
			"## These are revisions\n\nThe human reviewed commits and commented on or rejected some hunks. " +
				"Each task below carries those hunks of one commit with the comments on them. Work the revisions one " +
				"at a time, and run `diatom task done <id>` as soon as each is finished, before starting the next: " +
				"diatom splits your work at those points and commits each revision as a fixup of the commit it " +
				"revises. A comment may ask for no change once you look into it; say why with `diatom task note` " +
				"and mark the task done.\n\n",
		)
	case queue.Conflict:
		b.WriteString(
			"## A merge is in progress\n\nThe integration branch, or the base branch for a task below that " +
				"says so, is being merged into this workstream and some files conflict. Resolve every conflict (`git status` and `git diff` show them), keeping " +
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

// writeOthers lists the repo's other goals, so an agent knows what they
// cover and how far they have got without reading diatom's state.
func writeOthers(b *strings.Builder, in PromptInput) {
	var list strings.Builder
	_ = roster.Write(&list, in.Goals, in.Goal.Name)
	if list.Len() == 0 {
		return
	}
	b.WriteString(
		"## The repo's other goals\n\nOther work under way in this repo. Where yours touches one, " +
			"this is where it stands. `diatom task goals <name>` shows one in full: its plan, workstreams " +
			"and tasks. That command and this list are the only record of other goals: don't read " +
			"`.diatom/` or other worktrees for it, and don't take the repo's own notes or todo files as " +
			"their status.\n\n",
	)
	b.WriteString(list.String() + "\n")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// silentGuide keeps an agent from writing replies nobody reads.
const silentGuide = "- **Work silently.** Write no text between tool calls: no plan, no narration of " +
	"what you are about to do or have just done, and no recap at the end. Nobody reads it, and it costs " +
	"time. What the human should know goes through the task tool.\n"

// readGuide keeps an agent's turns few: each one re-reads the whole session.
const readGuide = "- **Read in few, whole pieces.** Every step re-reads the whole session so far, so " +
	"fewer, bigger reads cost less. Read a file you need whole, in one Read of up to 2,000 lines, and " +
	"not again; don't page through it with `sed -n`. In a big file, find the part you need with " +
	"`grep -n` or `rg -n -C 5` first. Commands already run in your working directory: don't `cd` to it " +
	"by its full path. Never search the whole disk with `find /`: a Go dependency's source is under " +
	"`go env GOMODCACHE`, and `go doc` shows its API.\n"

// askGuide is how every agent writes a question: the human answers it in a
// pane, without the code open, often long after it was asked.
const askGuide = "    Ask only what changes what gets built, how it is shaped or how good the code ends " +
	"up: structure, architecture, behaviour, and anything unclear in what is wanted. Never ask about " +
	"the order work is done in, and never ask the human to approve a choice you have reasoned through: " +
	"the human wants the work done, done well and with the least effort, not to sequence it. Make those " +
	"choices yourself and record the reasoning with `diatom task note`.\n" +
	"    Write each question so the human can answer it without the code open: say what it " +
	"decides and why it matters now, then give real examples from the repo for each option, such as " +
	"the card, function, file, command output or case it is about, quoted briefly, so the choice is " +
	"concrete rather than abstract. End with your recommended answer and why. Write plain sentences, " +
	"with no headings and no capitals for emphasis.\n"

// manualGuide is how an agent hands the human what only they can do.
const manualGuide = "  - `diatom task manual <id> \"<steps>\"` when the task needs the human to do " +
	"something by hand that you can't or mustn't, such as running `tofu apply`, signing in to a " +
	"service, or setting a secret. The task is parked until they say it is done, and their reply comes " +
	"back in the task text, so finish what you can first and leave the files passing the gate.\n" +
	"    Write numbered steps the human can follow without the code open: the exact commands, " +
	"where to run them, what to check in the output, and what to do if it fails. Say why each step " +
	"is needed, briefly, and never ask for a decision here: that is a question.\n"

// newGoalGuide is how a session other than triage starts a goal: only when
// the human asks for one.
const newGoalGuide = "  - `diatom task new-goal <id> -title \"<title>\" -description \"<line>\" " +
	"[-after <goals>] < brief` starts a new goal, grilled before any work starts. Use it only when the " +
	"human asks for a new goal, such as in answering your question; an idea of your own goes in a note " +
	"or a question instead. The description says in one plain sentence what the goal is for. The brief " +
	"on stdin is everything its grilling starts from: what the human asked, and what you found. -after " +
	"names the goals it must wait for, this one among them when it builds on this work.\n"

// orderGuide is how planning orders work, which it never asks the human
// about.
const orderGuide = "Order the work yourself, and don't ask about it: dependencies first, so a shared " +
	"piece lands before what uses it, then the simplest, lowest-risk change first within each step. Choose " +
	"the order that keeps the code clean at every step and the total effort lowest.\n\n"

// planningPrompt builds the instructions of a triage or grilling session.
func (h *Harness) planningPrompt(repo Repo, g *queue.Goal, in PromptInput) (string, error) {
	var b strings.Builder
	if in.Batch.Kind == queue.Triage {
		fmt.Fprintf(
			&b,
			"You are triaging what the human sent to this repo. Your working directory is a "+
				"read-only checkout of %s: read the code freely, but anything you write in it is thrown away.\n\n",
			g.Base,
		)
	} else {
		fmt.Fprintf(
			&b,
			"You are grilling the goal %q. Your working directory is a read-only checkout of "+
				"the goal's integration branch: read the code freely, but anything you write in it is thrown "+
				"away.\n\n",
			g.Title,
		)
	}
	b.WriteString("## How diatom works\n\n")
	b.WriteString(
		"- **Nobody reads your replies.** The session runs unattended: the human sees only what you " +
			"report with the task tool below. A question or result left in a reply is lost.\n",
	)
	b.WriteString(silentGuide)
	b.WriteString(readGuide)
	b.WriteString(
		"- You don't change code in this session. Report through the task tool, a shell command:\n",
	)
	b.WriteString(
		"  - `diatom task ask <id> \"<question>\"` asks the human something. The task waits for the " +
			"answer, which comes back in the task text.\n",
	)
	b.WriteString(askGuide)
	b.WriteString("  - `diatom task note <id> \"<text>\"` records something worth keeping.\n")
	b.WriteString("  - `diatom task done <id>` marks the task done.\n")
	if in.Batch.Kind != queue.Triage {
		b.WriteString(newGoalGuide)
	}
	if in.Batch.Kind == queue.Triage {
		if err := writeTriage(&b, repo, in.Goals); err != nil {
			return "", err
		}
	} else {
		writeGrilling(&b, repo, in)
		b.WriteString("\n")
		writeOthers(&b, in)
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

func writeTriage(b *strings.Builder, repo Repo, briefs []roster.Brief) error {
	b.WriteString(
		"  - `diatom task add-task <id> -goal <goal> -ws <workstream> -title \"<title>\" " +
			"[-after <ids>] [-profile <profile>] < body` adds a task to one of a goal's workstreams, with its " +
			"details on stdin. For a goal still being planned, it goes to the goal's grilling instead.\n",
	)
	b.WriteString(
		"  - `diatom task feedback <id> -goal <goal> < text` passes feedback to a goal being " +
			"planned: its grilling starts another round with it.\n",
	)
	b.WriteString(
		"  - `diatom task after <id> -goal <goal> -after <goal>,<goal>` makes a goal wait for others: " +
			"none of its work starts until they are finished, merged upstream. Name goals by name, or by " +
			"the title of one you start in this session. No -after clears it.\n",
	)
	b.WriteString(
		"  - `diatom task new-goal <id> -title \"<title>\" -description \"<line>\" [-after <goals>] " +
			"[-plan <plan.yaml>] < brief` starts a new goal, which is grilled before any work starts. The " +
			"description says in one plain sentence what the goal is for, beyond its title: it is how agents on " +
			"other goals, and the human answering its questions, tell it apart. The brief on stdin is " +
			"everything grilling starts from. Pass -plan only when the input already " +
			"decides everything, workstreams and tasks: the goal then skips grilling and waits for the human " +
			"to sign the plan off. The plan's format is below.\n\n",
	)
	b.WriteString(
		"## Triage\n\nEach task below is one intake: whatever the human sent, from a new goal " +
			"to a handful of playtest notes or a comment from review. Sort it into the repo's goals:\n\n" +
			"- Small, clear-cut work for an existing goal becomes tasks on its workstreams.\n" +
			"- New intent becomes a new goal, or several when the input covers separate things that land " +
			"apart. Draw each goal's brief from the input and the files it points at, so grilling " +
			"starts from everything it needs.\n" +
			"- Anything unclear, or that needs a design decision, is a question to the human.\n" +
			"- When goals would edit the same code, or the human orders them, make the later ones wait " +
			"for the earlier: goals that run side by side only meet when they land. Decide that order " +
			"yourself, as below; don't ask about it.\n\n" + orderGuide +
			"The goal the human was looking at when they sent it is noted in the intake: a hint, not a rule. " +
			"You can't add a workstream: ask instead. Mark each intake done once it is sorted.\n\n",
	)
	b.WriteString(
		"The plan -plan takes is YAML:\n\n```yaml\nsummary: What the goal does and how.\n" +
			"workstreams:\n  - name: engine\n  - name: cards\n    dependsOn: [engine]\ntasks:\n" +
			"  - key: ward\n    title: Add the ward keyword\n    workstream: engine\n    body: |\n" +
			"      What to do and how to know it's done.\n  - key: warden\n    title: Implement Warden\n" +
			"    workstream: cards\n    after: [ward]\n```\n\n",
	)
	goals, err := repo.Store.Goals()
	if err != nil {
		return err
	}
	b.WriteString("## The repo's goals\n\n")
	if len(goals) == 0 {
		b.WriteString("None yet.\n\n")
	}
	descriptions := map[string]string{}
	for _, br := range briefs {
		descriptions[br.Name] = br.Description
	}
	for _, g := range goals {
		if g.State == queue.GoalFinished {
			continue
		}
		if err := writeGoal(b, repo.Store, g, descriptions[g.Name]); err != nil {
			return err
		}
	}
	return nil
}

// writeGoal describes a goal for triage: its workstreams, and the tasks new
// work can be placed after.
func writeGoal(b *strings.Builder, s *queue.Store, g *queue.Goal, description string) error {
	fmt.Fprintf(b, "### %s: %s (%s)\n\n", g.Name, g.Title, g.State)
	if description != "" {
		b.WriteString(description + "\n\n")
	}
	if len(g.After) > 0 {
		fmt.Fprintf(b, "Waits for: %s\n\n", strings.Join(g.After, ", "))
	}
	if len(g.Workstreams) > 0 {
		b.WriteString("Workstreams:")
		for _, w := range g.Workstreams {
			fmt.Fprintf(b, " %s", w.Name)
			if len(w.DependsOn) > 0 {
				fmt.Fprintf(b, " (after %s)", strings.Join(w.DependsOn, ", "))
			}
		}
		b.WriteString("\n\n")
	}
	tasks, err := s.Tasks(g.Name)
	if err != nil {
		return err
	}
	open := 0
	for _, t := range tasks {
		if t.State == queue.Done || planningKind(t.Kind) {
			continue
		}
		open++
		fmt.Fprintf(b, "- %s [%s, %s] %s\n", t.ID, t.Workstream, t.State, t.Title)
	}
	if open > 0 {
		b.WriteString("\n")
	}
	return nil
}

func writeGrilling(b *strings.Builder, repo Repo, in PromptInput) {
	b.WriteString(
		"  - `diatom task plan <id> < plan.yaml` hands in the plan. It is checked straight away; " +
			"fix what it reports and hand it in again.\n\n",
	)
	b.WriteString(
		"## Grilling\n\nThe goal below hides decisions you can't make alone: what is new, how it " +
			"fits the code, how it should be structured. Find them before any work starts. If you have the " +
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
	b.WriteString(orderGuide)
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
