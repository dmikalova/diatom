// Package update keeps diatom on its latest release: it finds the release
// running now and the newest one published, installs the newest with
// `go install`, and hands the process over to it.
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

// Module is diatom's module, and Package its command.
const (
	Module  = "github.com/dmikalova/diatom"
	Package = Module + "/cmd/diatom"
)

// Current returns the release the running binary is, or "" when it was built
// from a checkout, which never updates itself: its builder is working on it.
func Current() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return current(bi)
}

func current(bi *debug.BuildInfo) string {
	for _, s := range bi.Settings {
		// Only a build from a checkout records its revision.
		if s.Key == "vcs.revision" {
			return ""
		}
	}
	if bi.Main.Path != Module || !valid(bi.Main.Version) {
		return ""
	}
	return bi.Main.Version
}

// Version is the version to show: the release, or "dev" and the revision
// for a build from a checkout.
func Version() string {
	if v := Current(); v != "" {
		return v
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var v strings.Builder
	v.WriteString("dev")
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			v.WriteString(" " + s.Value[:min(len(s.Value), 12)])
		case "vcs.modified":
			if s.Value == "true" {
				v.WriteString("+dirty")
			}
		}
	}
	return v.String()
}

// Go runs the go command in dir and returns its stdout.
type Go func(ctx context.Context, dir string, args ...string) ([]byte, error)

// RunGo runs go from PATH.
func RunGo(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		return out, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err,
			string(bytes.TrimSpace(exitErr.Stderr)))
	}
	return out, err
}

// Latest returns the newest release published, as the module proxy has it.
func Latest(ctx context.Context, goCmd Go) (string, error) {
	// Outside any module, so a go.mod around doesn't change the answer.
	out, err := goCmd(ctx, os.TempDir(), "list", "-m", "-json", Module+"@latest")
	if err != nil {
		return "", err
	}
	var m struct{ Version string }
	if err := json.Unmarshal(out, &m); err != nil {
		return "", fmt.Errorf("reading the latest version: %w", err)
	}
	if !valid(m.Version) {
		return "", fmt.Errorf("the latest version %q is not a release", m.Version)
	}
	return m.Version, nil
}

// Install installs version with go install and returns the new binary.
func Install(ctx context.Context, goCmd Go, version string) (string, error) {
	if _, err := goCmd(ctx, os.TempDir(), "install", Package+"@"+version); err != nil {
		return "", err
	}
	out, err := goCmd(ctx, os.TempDir(), "env", "GOBIN", "GOPATH")
	if err != nil {
		return "", err
	}
	gobin, gopath, _ := strings.Cut(string(bytes.TrimSpace(out)), "\n")
	dir := strings.TrimSpace(gobin)
	if dir == "" {
		first, _, _ := strings.Cut(strings.TrimSpace(gopath), string(os.PathListSeparator))
		dir = filepath.Join(first, "bin")
	}
	bin := filepath.Join(dir, "diatom")
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("go install left no diatom in %s: %w", dir, err)
	}
	return bin, nil
}

// Exec replaces the running process with bin, keeping its arguments and
// environment. It returns only if it fails.
func Exec(bin string) error {
	return syscall.Exec(bin, append([]string{bin}, os.Args[1:]...), os.Environ())
}

// Newer reports whether version a comes after b.
func Newer(a, b string) bool { return compare(a, b) > 0 }

// valid reports whether v is a semantic version such as v1.2.3 or
// v1.2.3-rc.1.
func valid(v string) bool {
	_, ok := parse(v)
	return ok
}

type version struct {
	core [3]int
	pre  string
}

func parse(v string) (version, bool) {
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return version{}, false
	}
	rest, _, _ = strings.Cut(rest, "+")
	core, pre, _ := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var out version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		out.core[i] = n
	}
	out.pre = pre
	return out, true
}

// compare orders versions the way semantic versioning does, comparing the
// pre-release text as a whole, which is enough for releases and Go's
// pseudo-versions.
func compare(a, b string) int {
	va, okA := parse(a)
	vb, okB := parse(b)
	switch {
	case !okA || !okB:
		return 0
	case va.core != vb.core:
		for i := range 3 {
			if va.core[i] != vb.core[i] {
				return va.core[i] - vb.core[i]
			}
		}
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	}
	return strings.Compare(va.pre, vb.pre)
}
