// Command diatom runs coding agents continuously through a priority queue of
// work while a human reviews their commits asynchronously. The design is in
// docs/adr/ and the vocabulary in CONTEXT.md.
//
// Usage:
//
//	diatom
//	diatom version
//
// diatom opens a window on the repository the current directory is in, and
// runs its scheduler until the window closes (ADR 0007).
//
// Inside an agent session:
//
//	diatom task done|note|ask <id> [text]
//	diatom task add-task|after|feedback|new-goal|plan <id> ...
//	diatom task goals [<goal>]
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
  diatom           open diatom on the repo; quitting suspends its sessions
  diatom version

Inside an agent session:
  diatom task done|note|ask <id> [text]
  diatom task add-task|after|feedback|new-goal|plan <id> ...
  diatom task goals [<goal>]
  diatom hook pre-tool-use|stop`
