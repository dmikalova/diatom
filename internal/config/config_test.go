package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tree lays out home/Code/org/repo with an XDG directory beside it.
func tree(t *testing.T) (root string, paths Paths) {
	t.Helper()
	base := t.TempDir()
	paths = Paths{Home: filepath.Join(base, "home"), XDG: filepath.Join(base, "xdg", "diatom")}
	root = filepath.Join(paths.Home, "Code", "org", "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, paths
}

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaults(t *testing.T) {
	root, paths := tree(t)
	c, err := Load(root, paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GateAttempts != 3 || c.MaxSessions != 1 || c.MaxBatch != 5 {
		t.Errorf("defaults = %+v", c.Repo)
	}
	p, err := c.Profile("implementation")
	if err != nil {
		t.Fatal(err)
	}
	if p.Effort != "medium" || p.Model != "opus" {
		t.Errorf("implementation profile = %+v", p)
	}
}

func TestLoadClosestWins(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, "gate = \"xdg-gate\"\nmaxSessions = 4\nautoUpdate = true\n"+
		"[profiles.implementation]\nmodel = \"sonnet\"\n")
	write(
		t,
		filepath.Join(paths.Home, "Code", "org", DirName),
		"gate = \"org-gate\"\ncommitCheck = \"lint\"\n",
	)
	write(t, filepath.Join(root, DirName), "gate = \"mage check\"\n[adr]\ndir = \"docs/adr\"\n")

	c, err := Load(root, paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Gate != "mage check" {
		t.Errorf("Gate = %q, want the repo's", c.Gate)
	}
	if c.CommitCheck != "lint" {
		t.Errorf("CommitCheck = %q, want the org directory's", c.CommitCheck)
	}
	if c.MaxSessions != 4 {
		t.Errorf("MaxSessions = %d, want the XDG file's", c.MaxSessions)
	}
	if c.ADR.Dir != "docs/adr" {
		t.Errorf("ADR.Dir = %q", c.ADR.Dir)
	}
	p := c.Profiles["implementation"]
	if p.Model != "sonnet" || p.Effort != "medium" || p.MaxTurns != 300 {
		t.Errorf("implementation = %+v, want the model overridden and the rest kept", p)
	}
	if !c.AutoUpdate {
		t.Error("AutoUpdate from the XDG file was lost")
	}
}

func TestLoadStopsAtHome(t *testing.T) {
	root, paths := tree(t)
	write(t, filepath.Join(filepath.Dir(paths.Home), DirName), "gate = \"above-home\"\n")
	c, err := Load(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Gate != "" {
		t.Errorf("Gate = %q, want the file above home ignored", c.Gate)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, dir, body, want string
	}{
		{"home-only key in a repo", "repo", "[profiles]\n", "profiles is only read from"},
		{"unknown key", "repo", "gaet = \"x\"\n", "unknown setting gaet"},
		{"unknown nested key", "repo", "[adr]\nfolder = \"x\"\n", "unknown setting adr.folder"},
		{"invalid TOML", "xdg", "gate = [\n", "config.toml"},
		{"bad autoApprove pattern", "repo", "autoApprove = [\"[\"]\n", "autoApprove \"[\""},
		{"unknown kind", "repo", "[gates]\ncobol = \"make\"\n", "gates.cobol: no such kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, paths := tree(t)
			dir := filepath.Join(root, DirName)
			if tt.dir == "xdg" {
				dir = paths.XDG
			}
			write(t, dir, tt.body)
			_, err := Load(root, paths)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadHome(t *testing.T) {
	_, paths := tree(t)
	write(t, paths.XDG, "autoUpdate = true\n")
	c, err := LoadHome(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !c.AutoUpdate {
		t.Error("AutoUpdate = false")
	}
}

func TestProfileMissing(t *testing.T) {
	c := &Config{}
	if _, err := c.Profile("nope"); err == nil {
		t.Error("Profile of a missing name returned no error")
	}
}

func TestPathsExpand(t *testing.T) {
	p := Paths{Home: "/home/me"}
	for in, want := range map[string]string{
		"~":           "/home/me",
		"~/AGENTS.md": "/home/me/AGENTS.md",
		"/etc/x":      "/etc/x",
		"rel/x":       "rel/x",
		"~other/x":    "~other/x",
	} {
		if got := p.Expand(in); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := p.SkillDir("grilling"); got != "/home/me/.claude/skills/grilling" {
		t.Errorf("SkillDir(grilling) = %q", got)
	}
	if got := p.SkillDir("~/skills/mine"); got != "/home/me/skills/mine" {
		t.Errorf("SkillDir(path) = %q", got)
	}
}

func TestLoadContextDefaults(t *testing.T) {
	root, paths := tree(t)
	write(t, filepath.Join(root, DirName), "[mcpServers.docs]\ncommand = \"docs-mcp\"\n")
	c, err := Load(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Instructions) != 1 || c.Instructions[0] != "~/AGENTS.md" {
		t.Errorf("Instructions = %v", c.Instructions)
	}
	if s := c.Profiles["planning"].Skills; len(s) != 1 || s[0] != "grilling" {
		t.Errorf("planning skills = %v", s)
	}
	if _, ok := c.MCPServers["docs"]; !ok {
		t.Errorf("MCPServers = %v", c.MCPServers)
	}
}

// TestRetryEffort pins that a retry thinks one step harder but never climbs
// to max effort (ADR 0005).
func TestRetryEffort(t *testing.T) {
	for in, want := range map[string]string{
		"": "high", "low": "medium", "medium": "high", "high": "xhigh", "xhigh": "xhigh", "max": "max",
		"odd": "odd",
	} {
		if got := RetryEffort(in); got != want {
			t.Errorf("RetryEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTimeouts(t *testing.T) {
	root, paths := tree(t)
	cfg, err := Load(root, paths)
	if err != nil || cfg.CommandTimeout != 30*time.Second || cfg.GateTimeout != 2*time.Minute {
		t.Fatalf("defaults = %v and %v, %v", cfg.CommandTimeout, cfg.GateTimeout, err)
	}
	write(t, filepath.Join(root, DirName), "commandTimeout = \"1m\"\ngateTimeout = \"3m30s\"\n")
	if cfg, err := Load(root, paths); err != nil || cfg.CommandTimeout != time.Minute ||
		cfg.GateTimeout != 210*time.Second {
		t.Errorf("repo override = %v and %v, %v", cfg.CommandTimeout, cfg.GateTimeout, err)
	}
}

func TestGateByKind(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, "[gates]\ngo = \"mage ci:check\"\nnode = \"npm test\"\n")
	if cfg, err := Load(root, paths); err != nil || cfg.Gate != "" {
		t.Errorf("a repo of no kind = %q, %v", cfg.Gate, err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(root, paths); err != nil || cfg.Gate != "mage ci:check" {
		t.Errorf("a Go repo = %q, %v", cfg.Gate, err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, paths); err == nil || !strings.Contains(err.Error(), "set gate in") {
		t.Errorf("a Go and node repo = %v, want it told to set its own gate", err)
	}
	if err := SetGate(root, "make check"); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(root, paths); err != nil || cfg.Gate != "make check" {
		t.Errorf("after SetGate = %q, %v", cfg.Gate, err)
	}
}

func TestSetGateKeepsTheRest(t *testing.T) {
	root, paths := tree(t)
	write(t, filepath.Join(root, DirName), "maxSessions = 2\n\n[adr]\ndir = \"docs/adr\"\n")
	if err := SetGate(root, `mage "ci:check"`); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root, paths)
	if err != nil || cfg.Gate != `mage "ci:check"` || cfg.MaxSessions != 2 ||
		cfg.ADR.Dir != "docs/adr" {
		t.Errorf("after SetGate = %+v, %v", cfg, err)
	}
}

func TestOldYAMLIsAnError(t *testing.T) {
	root, paths := tree(t)
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("gate: x\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, paths); err == nil || !strings.Contains(err.Error(), "TOML now") {
		t.Errorf("Load = %v", err)
	}
}
