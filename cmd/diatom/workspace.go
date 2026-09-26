package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/focus"
	"github.com/dmikalova/diatom/internal/panes"
	"github.com/dmikalova/diatom/internal/registry"
)

// sessionName is the zellij session the workspace lives in.
const sessionName = "diatom"

// cmdWorkspace opens the workspace (ADR 0007): a zellij session you can
// detach from and reattach to, with the reviewer, status, questions and intake
// panes, and the scheduler in a tab of its own. An existing session is
// reattached.
func cmdWorkspace(ctx context.Context) error {
	if os.Getenv("ZELLIJ") != "" {
		return errors.New(
			"already inside zellij: detach first, or run `zellij attach " + sessionName + "`",
		)
	}
	zellij, err := exec.LookPath("zellij")
	if err != nil {
		return fmt.Errorf("the workspace runs in zellij, which is not installed: %w", err)
	}
	out, err := exec.CommandContext(ctx, zellij, "list-sessions", "--short", "--no-formatting").
		Output()
	if err == nil && slices.Contains(strings.Fields(string(out)), sessionName) {
		return syscall.Exec(zellij, []string{"zellij", "attach", sessionName}, os.Environ())
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	reg, err := registry.Default()
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(reg.Path), "workspace.kdl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(workspaceLayout(exe)), 0o644); err != nil {
		return err
	}
	return syscall.Exec(
		zellij,
		[]string{"zellij", "--session", sessionName, "--new-session-with-layout", path},
		os.Environ(),
	)
}

// workspaceLayout is the zellij layout of the workspace, running exe.
func workspaceLayout(exe string) string {
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	pane := func(indent, name, size string, focus bool, args ...string) string {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = q(a)
		}
		attrs := fmt.Sprintf("name=%s command=%s", q(name), q(exe))
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
		pane("                ", "questions", "35%", false, "pane", "questions") +
		pane("                ", "intake", "20%", false, "pane", "intake") +
		"            }\n" +
		"        }\n" +
		"    }\n" +
		"    tab name=\"scheduler\" {\n" +
		pane("        ", "scheduler", "", false, "run") +
		"    }\n" +
		"}\n"
}

// paneEnv is what the workspace panes share.
func paneEnv() (panes.Env, error) {
	reg, err := registry.Default()
	if err != nil {
		return panes.Env{}, err
	}
	fc, err := focus.Default()
	if err != nil {
		return panes.Env{}, err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return panes.Env{}, err
	}
	return panes.Env{Registry: reg, Focus: fc, Paths: paths, Now: time.Now}, nil
}

// cmdPane runs one of the workspace's panes.
func cmdPane(ctx context.Context, args []string, _ io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: pane takes status, questions or intake", errUsage)
	}
	env, err := paneEnv()
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
