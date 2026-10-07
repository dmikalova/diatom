// Command diatom runs coding agents continuously through a priority queue of
// work while a human reviews their commits asynchronously. The design is in
// docs/adr/ and the vocabulary in CONTEXT.md.
//
// Usage:
//
//	diatom
//	diatom ledger
//	diatom version
//	diatom help
//
// diatom opens a window on the repository the current directory is in, and
// runs its scheduler until the window closes (ADR 0007).
//
// Inside an agent session:
//
//	diatom task done|note|ask|manual <id> [text]
//	diatom task connect <id> <connector> <why>
//	diatom task ci <id> [--label <name>]... <why>
//	diatom task goals [<goal>]
//	diatom task add-task|after|feedback|new-goal|plan <id> ...
//	diatom hook pre-tool-use|stop
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
		args = []string{"open"}
	}
	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "open":
		err = cmdApp(ctx)
	case "task":
		err = cmdTask(ctx, rest, stdin, stdout)
	case "hook":
		err = cmdHook(ctx, rest, stdin, stdout)
	case "ledger":
		err = cmdLedger(ctx, stdout)
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
  diatom                 open diatom on the repo; quitting suspends its sessions
  diatom ledger          the repo's landed goals: lines of code for each dollar
  diatom version         the version running
  diatom help            this

Inside an agent session, reporting on the task you were given:
  diatom task done <id> [what you did]
  diatom task note <id> <text>             add to the task's body
  diatom task ask <id> <question>          park the task on a question
  diatom task manual <id> <steps>          park it on steps only the human can do
  diatom task connect <id> <connector> <why>
                                           ask for one of the repo's MCP servers;
                                           the session ends and runs again with it
  diatom task ci <id> [--label <name>]... <why>
                                           push the goal's branch, open its pull
                                           request and bring back its checks;
                                           a label goes on for the run only
  diatom task goals [<goal>]               the repo's goals, or one in full

From a triage session, except new-goal, which any session may run:
  diatom task add-task <id> -goal <goal> -ws <ws> -title <title>
                       [-after <ids>] [-profile <name>] < body
  diatom task feedback <id> -goal <goal> < text
  diatom task after <id> -goal <goal> [-after <goals>]
  diatom task new-goal <id> -title <title> -description <line>
                       [-after <goals>] [-branch <name>] [-ticket <id>]
                       [-plan <file.yaml>] < brief

From a grilling session:
  diatom task plan <id> < plan.yaml        hand in the plan for sign-off

Run by Claude Code, not by you:
  diatom hook pre-tool-use|stop`
