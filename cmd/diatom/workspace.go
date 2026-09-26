package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/panes"
)

// sessionName is the zellij session of the repo's workspace. Each repo has
// its own, so the workspaces of two repos can be open at once.
func sessionName(root string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' ||
			r == '_' {
			return r
		}
		return '-'
	}, filepath.Base(root))
	return "diatom-" + name
}

// cmdWorkspace opens the repo's workspace (ADR 0007): a zellij session you
// can detach from and reattach to, with the reviewer, status, questions and
// intake panes, and the scheduler in a tab of its own. An existing session is
// reattached.
func cmdWorkspace(ctx context.Context) error {
	s, err := here(ctx)
	if err != nil {
		return err
	}
	sessionName := sessionName(s.Repo())
	if os.Getenv("ZELLIJ") != "" {
		return errors.New(
			"already inside zellij: detach first, or run `zellij attach " + sessionName + "`",
		)
	}
	zellij, err := exec.LookPath("zellij")
	if err != nil {
		return fmt.Errorf("the workspace runs in zellij, which is not installed: %w", err)
	}
	out, _ := exec.CommandContext(ctx, zellij, "list-sessions", "--no-formatting").Output()
	switch sessionState(string(out), sessionName) {
	case sessionRunning:
		return syscall.Exec(zellij, []string{"zellij", "attach", sessionName}, os.Environ())
	case sessionExited:
		// Resurrected, it would hold every pane at "Waiting to run", and on the
		// layout it had: a fresh session runs them, on the layout of now.
		if err := exec.CommandContext(ctx, zellij, "delete-session", sessionName).
			Run(); err != nil {
			return fmt.Errorf("removing the exited workspace session %s: %w", sessionName, err)
		}
	}
	exe, err := self()
	if err != nil {
		return err
	}
	path := filepath.Join(s.Root, "workspace.kdl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(workspaceLayout(exe, s.Repo())), 0o644); err != nil {
		return err
	}
	return syscall.Exec(
		zellij,
		[]string{"zellij", "--session", sessionName, "--new-session-with-layout", path},
		os.Environ(),
	)
}

// The states a zellij session can be in.
const (
	sessionMissing = iota
	sessionRunning
	sessionExited
)

// sessionState finds a session in `zellij list-sessions --no-formatting`
// output, where an exited one is marked EXITED.
func sessionState(list, name string) int {
	for line := range strings.SplitSeq(list, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == name {
			if strings.Contains(line, "EXITED") {
				return sessionExited
			}
			return sessionRunning
		}
	}
	return sessionMissing
}

// workspaceLayout is the zellij layout of the workspace of the repo at root,
// running exe. Every pane starts in the repo, which is how it finds it.
func workspaceLayout(exe, root string) string {
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	pane := func(indent, name, size string, focus bool, args ...string) string {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = q(a)
		}
		// A pane without a name shows the title its program sets, such as the
		// questions pane's count.
		attrs := "command=" + q(exe)
		if name != "" {
			attrs = "name=" + q(name) + " " + attrs
		}
		if size != "" {
			attrs += " size=" + q(size)
		}
		if focus {
			attrs += " focus=true"
		}
		return fmt.Sprintf(
			"%spane %s {\n%s    args %s\n%s}\n",
			indent,
			attrs,
			indent,
			strings.Join(quoted, " "),
			indent,
		)
	}
	return "layout {\n" +
		"    cwd " + q(root) + "\n" +
		"    default_tab_template {\n" +
		"        pane size=1 borderless=true {\n            plugin location=\"zellij:tab-bar\"\n        }\n" +
		"        children\n" +
		"        pane size=2 borderless=true {\n            plugin location=\"zellij:status-bar\"\n        }\n" +
		"    }\n" +
		"    tab name=\"work\" focus=true {\n" +
		"        pane split_direction=\"vertical\" {\n" +
		pane("            ", "review", "60%", true, "review", "-focus") +
		"            pane split_direction=\"horizontal\" {\n" +
		pane("                ", "status", "45%", false, "pane", "status") +
		pane("                ", "", "35%", false, "pane", "questions") +
		pane("                ", "intake", "20%", false, "pane", "intake") +
		"            }\n" +
		"        }\n" +
		"    }\n" +
		"    tab name=\"scheduler\" {\n" +
		pane("        ", "scheduler", "", false, "run") +
		"    }\n" +
		"}\n"
}

// paneEnv is what the workspace panes of the repo share.
func paneEnv(ctx context.Context) (panes.Env, error) {
	s, err := here(ctx)
	if err != nil {
		return panes.Env{}, err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return panes.Env{}, err
	}
	return panes.Env{Store: s, Focus: focus.In(s.Repo()), Paths: paths, Now: time.Now}, nil
}

// cmdPane runs one of the workspace's panes.
func cmdPane(ctx context.Context, args []string, _ io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: pane takes status, questions or intake", errUsage)
	}
	env, err := paneEnv(ctx)
	if err != nil {
		return err
	}
	var m tea.Model
	switch args[0] {
	case "status":
		m = panes.NewStatus(ctx, env)
	case "questions":
		m = panes.NewQuestions(env)
	case "intake":
		m = panes.NewIntake(env)
	default:
		return fmt.Errorf("%w: unknown pane %q", errUsage, args[0])
	}
	return runProgram(ctx, m)
}
