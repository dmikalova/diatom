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
	if c.GateAttempts != 3 || c.MaxSessions != 1 || c.MaxBatch != 5 || c.ChainContext != 100000 {
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
	write(t, paths.XDG, "skills = [\"grill-me\", \"grilling\"]\n")
	write(t, filepath.Join(root, DirName), "[mcpServers.docs]\ncommand = \"docs-mcp\"\n")
	c, err := Load(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Instructions) != 0 {
		t.Errorf(
			"Instructions = %v, want none: AGENTS.md files are found by the walk",
			c.Instructions,
		)
	}
	if len(c.Skills) != 2 || c.Skills[0] != "grill-me" {
		t.Errorf("Skills = %v", c.Skills)
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
	if err != nil || cfg.CommandTimeout != 2*time.Minute || cfg.GateTimeout != 5*time.Minute {
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
	if _, err := Load(root, paths); err == nil || !strings.Contains(err.Error(), "set its own in") {
		t.Errorf("a Go and node repo = %v, want it told to set its own gate", err)
	}
	if err := SetGate(root, "make check"); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(root, paths); err != nil || cfg.Gate != "make check" {
		t.Errorf("after SetGate = %q, %v", cfg.Gate, err)
	}
}

func TestFixByKind(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, "[gates]\ngo = \"mage ci:check\"\n[fixes]\ngo = \"mage ci:fix\"\n")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root, paths)
	if err != nil || cfg.Fix != "mage ci:fix" || cfg.SessionGate() != "mage ci:fix; mage ci:check" {
		t.Errorf("fix = %q, session gate %q, %v", cfg.Fix, cfg.SessionGate(), err)
	}
	if cfg := (&Config{Gate: "make check"}); cfg.SessionGate() != "make check" {
		t.Errorf("without a fix, the session gate is %q", cfg.SessionGate())
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

func TestBudget(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, "[budget]\nmonth = 2000\n")
	write(t, filepath.Join(root, DirName), "[budget]\nday = 220\nweek = 800.5\n")
	cfg, err := Load(root, paths)
	if err != nil || cfg.Budget != (Budget{Day: 220, Week: 800.5, Month: 2000}) {
		t.Errorf("budget = %+v, %v", cfg.Budget, err)
	}
}

func TestCovers(t *testing.T) {
	all := []string{"Read", "Edit", "Bash", "Skill"}
	c := &Config{Profiles: map[string]Profile{
		"implementation": {Model: "opus", Effort: "medium", Tools: all},
		"mechanical":     {Model: "sonnet", Effort: "medium", Tools: all[:3]},
		"deep":           {Model: "claude-sonnet-5", Effort: "high", Tools: all},
		"readonly":       {Model: "opus", Tools: all[:1]},
		"skilled":        {Model: "sonnet", Tools: all[:1], Skills: []string{"grilling"}},
		"custom":         {Model: "my-model", Tools: all[:1]},
	}}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"implementation", "mechanical", true},
		{"mechanical", "implementation", false}, // a weaker model
		{"implementation", "deep", false},       // less effort
		{"deep", "mechanical", true},
		{"readonly", "mechanical", false}, // fewer tools
		{"implementation", "skilled", false},
		{"implementation", "custom", false}, // a model of no known family
		{"custom", "custom", true},
		{"implementation", "missing", false},
	} {
		if got := c.Covers(tc.a, tc.b); got != tc.want {
			t.Errorf("Covers(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLand(t *testing.T) {
	root, paths := tree(t)
	write(t, filepath.Join(root, DirName), "land = \"merge\"\n")
	if cfg, err := Load(root, paths); err != nil || cfg.Land != LandMerge {
		t.Errorf("land = %+v, %v", cfg, err)
	}
	write(t, filepath.Join(root, DirName), "land = \"push\"\n")
	if _, err := Load(root, paths); err == nil || !strings.Contains(err.Error(), "land") {
		t.Errorf("land = push loaded: %v", err)
	}
}
