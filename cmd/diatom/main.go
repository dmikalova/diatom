// Command diatom runs coding agents continuously through a priority queue of
// work while a human reviews their commits asynchronously. The design is in
// docs/adr/ and the vocabulary in CONTEXT.md.
//
// Usage:
//
//	diatom run
//	diatom stop [-drain]
//	diatom status
//	diatom goal new <name> [-title text] [-after goal,goal] < description
//	diatom goal new <name> [-title text] -ws engine,cards:engine -active
//	diatom goal plan|approve <name>
//	diatom goal list
//	diatom goal activate|park <name>
//	diatom goal after <name> [goal...]
//	diatom goal done <name> [-force]
//	diatom goal finish <name> [-push | -prs] [-remote origin] [-force]
//	diatom task add -goal <goal> -ws <workstream> [-kind planned] [-profile name]
//	                [-after id,id] [-priority n] <title> < body.md
//	diatom questions
//	diatom review [-goal <goal>] [-list] [-focus]
//	diatom workspace
//	diatom pane status|questions|intake
//	diatom answer <goal> <question> <answer>
//	diatom intake [-goal <goal>] < text
//	diatom version
//
// Inside an agent session:
//
//	diatom task done <id>
//	diatom task note <id> <text>
//	diatom task ask <id> <question>
//	diatom hook pre-tool-use
//	diatom hook stop
//
// The goal, task, questions and answer commands act on the repository the
// current directory is in.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmikalova/diatom/internal/update"
)

// version is the release version, set by goreleaser with -ldflags.
var version = "dev"

func main() {
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(stop, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

// errUsage marks an error whose fix is reading the usage.
var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "run":
		err = cmdRun(ctx, stderr)
	case "stop":
		err = cmdStop(ctx, rest, stdout)
	case "status":
		err = cmdStatus(ctx, stdout)
	case "goal":
		err = cmdGoal(ctx, rest, stdin, stdout)
	case "task":
		err = cmdTask(ctx, rest, stdin, stdout)
	case "questions":
		err = cmdQuestions(ctx, stdout)
	case "review":
		err = cmdReview(ctx, rest, stdout)
	case "workspace":
		err = cmdWorkspace(ctx)
	case "pane":
		err = cmdPane(ctx, rest, stdout)
	case "answer":
		err = cmdAnswer(ctx, rest)
	case "intake":
		err = cmdIntake(ctx, rest, stdin, stdout)
	case "hook":
		err = cmdHook(ctx, rest, stdin, stdout)
	case "version":
		v := version
		if v == "dev" {
			v = update.Version()
		}
		_, _ = fmt.Fprintln(stdout, v)
	case "help", "-h", "--help":
		_, _ = fmt.Fprintln(stdout, usage)
	default:
		err = fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "diatom:", err)
	if errors.Is(err, errUsage) {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	return 1
}

const usage = `Usage:
  diatom run                        run the scheduler; Ctrl-C suspends its sessions
  diatom stop [-drain]              suspend the running scheduler, or let it drain
  diatom status                     show every goal in the repo
  diatom goal new <name> [-title text] [-after a,b] < description   grill a new goal
  diatom goal new <name> -ws engine,cards:engine -active   skip grilling
  diatom goal plan|approve <name>   show or sign off a goal's plan
  diatom goal list
  diatom goal activate|park <name>
  diatom goal after <name> [goal...]   wait for those goals to finish; none clears it
  diatom goal done <name> [-force]  finish a goal and lay it out for landing
  diatom goal finish <name> [-push | -prs] [-remote origin]   land a done goal
  diatom task add -goal <goal> -ws <workstream> [-kind planned] [-profile name]
                  [-after id,id] [-priority n] <title> < body.md
  diatom questions                  list open questions
  diatom review [-goal g] [-list]   review the agents' commits, hunk by hunk
  diatom workspace                  open the zellij workspace with every pane
  diatom pane status|questions|intake   run one workspace pane
  diatom answer <goal> <question> <answer>
  diatom intake [-goal <goal>] < text   send anything for triage to sort out
  diatom version

Inside an agent session:
  diatom task done|note|ask <id> [text]
  diatom hook pre-tool-use|stop`
