package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"v0.4.0", "v0.3.0", true},
		{"v0.3.0", "v0.4.0", false},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.1-0.20260926000000-abcdef", "v1.0.0", true},
		{"v1.0.0", "(devel)", false},
	} {
		if got := Newer(tt.a, tt.b); got != tt.want {
			t.Errorf("Newer(%s, %s) = %v", tt.a, tt.b, got)
		}
	}
}

func TestCurrent(t *testing.T) {
	release := &debug.BuildInfo{Main: debug.Module{Path: Module, Version: "v0.3.0"}}
	if got := current(release); got != "v0.3.0" {
		t.Errorf("a release install = %q", got)
	}
	checkout := &debug.BuildInfo{
		Main:     debug.Module{Path: Module, Version: "v0.3.1-0.20260926000000-abcdef"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef"}},
	}
	if got := current(checkout); got != "" {
		t.Errorf("a build from a checkout = %q, want it never to update", got)
	}
	if got := current(
		&debug.BuildInfo{Main: debug.Module{Path: Module, Version: "(devel)"}},
	); got != "" {
		t.Errorf("a devel build = %q", got)
	}
}

func TestLatestAndInstall(t *testing.T) {
	gobin := t.TempDir()
	var calls []string
	fake := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "list":
			return []byte(`{"Path":"github.com/dmikalova/diatom","Version":"v0.4.0"}`), nil
		case "install":
			return nil, os.WriteFile(filepath.Join(gobin, "diatom"), nil, 0o755)
		}
		return []byte(gobin + "\n/go\n"), nil
	}
	v, err := Latest(context.Background(), fake)
	if err != nil || v != "v0.4.0" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
	bin, err := Install(context.Background(), fake, v)
	if err != nil || bin != filepath.Join(gobin, "diatom") {
		t.Fatalf("Install = %q, %v", bin, err)
	}
	if calls[1] != "install "+Package+"@v0.4.0" {
		t.Errorf("calls = %v", calls)
	}
}
