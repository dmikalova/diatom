package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tree lays out home/Code/org/repo with an XDG directory beside it, and the
// key the config names that repo by.
func tree(t *testing.T) (root string, paths Paths) {
	t.Helper()
	base := t.TempDir()
	paths = Paths{
		Home: filepath.Join(base, "home"),
		XDG:  filepath.Join(base, "xdg", "diatom"),
		Key:  "github.com/org/repo",
	}
	root = filepath.Join(paths.Home, "Code", "org", "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, paths
}

// block is a [repos."<prefix>"] block of the config file. A table inside it
// is written out in full, as TOML otherwise reads it as a table of its own.
func block(prefix, body string) string {
	head := fmt.Sprintf("[repos.%q]", prefix)
	var out []string
	for line := range strings.SplitSeq(body, "\n") {
		if rest, ok := strings.CutPrefix(line, "["); ok {
			line = fmt.Sprintf("[repos.%q.%s", prefix, rest)
		}
		out = append(out, line)
	}
	return "\n" + head + "\n" + strings.Join(out, "\n")
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

// TestTheLongestBlockWins pins that a repo's own block is read over its
// organisation's, which is read over the top of the file (ADR 0013).
func TestTheLongestBlockWins(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, "gate = \"xdg-gate\"\nmaxSessions = 4\nautoUpdate = true\n"+
		"[profiles.implementation]\nmodel = \"sonnet\"\n"+
		block("github.com/org", "gate = \"org-gate\"\ncommitCheck = \"lint\"\n")+
		block("github.com/org/repo", "gate = \"mage check\"\n[adr]\ndir = \"docs/adr\"\n"))

	c, err := Load(root, paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Gate != "mage check" {
		t.Errorf("Gate = %q, want the repo's", c.Gate)
	}
	if c.CommitCheck != "lint" {
		t.Errorf("CommitCheck = %q, want the organisation's", c.CommitCheck)
	}
	if c.MaxSessions != 4 {
		t.Errorf("MaxSessions = %d, want the top of the file's", c.MaxSessions)
	}
	if c.ADR.Dir != "docs/adr" {
		t.Errorf("ADR.Dir = %q", c.ADR.Dir)
	}
	p := c.Profiles["implementation"]
	if p.Model != "sonnet" || p.Effort != "medium" || p.MaxTurns != 300 {
		t.Errorf("implementation = %+v, want the model overridden and the rest kept", p)
	}
	if !c.AutoUpdate {
		t.Error("AutoUpdate from the top of the file was lost")
	}
}

// TestABlockOfAnotherRepoIsNotRead pins that a prefix only covers the repos
// under it.
func TestABlockOfAnotherRepoIsNotRead(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, block("github.com/other", "gate = \"theirs\"\n")+
		block("github.com/org/repository", "gate = \"nearly\"\n"))
	c, err := Load(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Gate != "" {
		t.Errorf("Gate = %q, want neither block read", c.Gate)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"home-only key in a block", block("github.com/org", "[profiles]\n"),
			"which is yours and not a repo's"},
		{"unknown key", "gaet = \"x\"\n", "unknown setting gaet"},
		{"unknown nested key", "[adr]\nfolder = \"x\"\n", "unknown setting adr.folder"},
		{"invalid TOML", "gate = [\n", "config.toml"},
		{"bad autoApprove pattern", "autoApprove = [\"[\"]\n", "autoApprove \"[\""},
		{"unknown kind", "[gates]\ncobol = \"make\"\n", "gates.cobol: no such kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, paths := tree(t)
			write(t, paths.XDG, tt.body)
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
	write(t, paths.XDG, "skills = [\"grill-me\", \"grilling\"]\n"+
		block("github.com/org/repo",
			"[mcpServers.docs]\ncommand = \"docs-mcp\"\npurpose = \"the API docs\"\n"))
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

// TestCatalogComesFromTheReposMCPFile pins that a repo declares its servers
// once, in Claude Code's own file, and diatom's config only adds a purpose
// (ADR 0013).
func TestCatalogComesFromTheReposMCPFile(t *testing.T) {
	root, paths := tree(t)
	if err := os.WriteFile(filepath.Join(root, MCPFileName), []byte(
		`{"mcpServers":{"linear":{"type":"sse","url":"https://mcp.linear.app/sse"},`+
			`"docs":{"command":"docs-mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, paths.XDG,
		block("github.com/org/repo", "[mcpServers.linear]\npurpose = \"the tickets\"\n"))
	c, err := Load(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	cat := c.Connectors()
	if len(cat) != 2 || cat[0].Name != "docs" || cat[0].Purpose != "" ||
		cat[1].Name != "linear" || cat[1].Purpose != "the tickets" {
		t.Fatalf("catalog = %+v, want both servers, the purpose on linear", cat)
	}
	// Diatom's purpose must not cost the server what .mcp.json said it is.
	srv, err := c.MCPConfig([]string{"linear"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := srv["linear"].(map[string]any)
	if m["url"] != "https://mcp.linear.app/sse" || m[PurposeKey] != nil {
		t.Errorf("linear = %v, want .mcp.json's own fields without the purpose", m)
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
	write(t, paths.XDG,
		block("github.com/org/repo", "commandTimeout = \"1m\"\ngateTimeout = \"3m30s\"\n"))
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
	if err := Set(paths, paths.Key, map[string]any{"gate": "make check"}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(root, paths); err != nil || cfg.Gate != "make check" {
		t.Errorf("after Set = %q, %v", cfg.Gate, err)
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

// TestSetKeepsTheRest pins that writing one lever leaves the rest of the
// repo's block alone (ADR 0013).
func TestSetKeepsTheRest(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG,
		block("github.com/org/repo", "maxSessions = 2\n[adr]\ndir = \"docs/adr\"\n"))
	if err := Set(paths, paths.Key, map[string]any{"gate": `mage "ci:check"`}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root, paths)
	if err != nil || cfg.Gate != `mage "ci:check"` || cfg.MaxSessions != 2 ||
		cfg.ADR.Dir != "docs/adr" {
		t.Fatalf("after Set = %+v, %v", cfg, err)
	}
	own, err := Own(paths, paths.Key)
	if err != nil || own["gate"] != `mage "ci:check"` {
		t.Errorf("the repo's own keys = %v, %v", own, err)
	}
	if err := Set(paths, paths.Key, map[string]any{"gate": nil}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(root, paths); err != nil || cfg.Gate != "" || cfg.MaxSessions != 2 {
		t.Errorf("after clearing the gate = %+v, %v", cfg, err)
	}
}

// TestSetRefusesAHomeKeyInABlock pins that what is the human's is set once,
// at the top of the file (ADR 0013).
func TestSetRefusesAHomeKeyInABlock(t *testing.T) {
	_, paths := tree(t)
	err := Set(paths, paths.Key, map[string]any{"autoUpdate": true})
	if err == nil || !strings.Contains(err.Error(), "is yours, not a repo's") {
		t.Errorf("Set of a home key in a block = %v", err)
	}
}

// TestEnsureWritesTheFileOnce pins that diatom writes the config the first
// time, annotated, and leaves an existing one alone (ADR 0013).
func TestEnsureWritesTheFileOnce(t *testing.T) {
	_, paths := tree(t)
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(File(paths))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("[repos.\"<prefix>\"]")) {
		t.Errorf("the written config does not explain the blocks:\n%s", b)
	}
	if err := Set(paths, paths.Key, map[string]any{"gate": "check"}); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	own, err := Own(paths, paths.Key)
	if err != nil || own["gate"] != "check" {
		t.Errorf("Ensure overwrote the file: %v, %v", own, err)
	}
}

// TestUnknownKeysAreKept pins that a key diatom does not know survives a
// write, rather than being quietly dropped (ADR 0013).
func TestUnknownKeysAreKept(t *testing.T) {
	_, paths := tree(t)
	write(t, paths.XDG, block("github.com/org/repo", "gaet = \"x\"\n"))
	if err := Set(paths, paths.Key, map[string]any{"gate": "check"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(File(paths))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("does not know these keys")) ||
		!bytes.Contains(b, []byte("gaet")) {
		t.Errorf("the unknown key was dropped:\n%s", b)
	}
}

// TestValueNamesALever pins that the window can ask what any lever is.
func TestValueNamesALever(t *testing.T) {
	c := &Config{Gate: "check", MaxSessions: 3}
	if v, ok := c.Value("gate"); !ok || v != "check" {
		t.Errorf("Value(gate) = %v, %v", v, ok)
	}
	if v, ok := c.Value("maxSessions"); !ok || v != 3 {
		t.Errorf("Value(maxSessions) = %v, %v", v, ok)
	}
	if _, ok := c.Value("nope"); ok {
		t.Error("Value of a key that is not a lever said it was one")
	}
}

func TestBudget(t *testing.T) {
	root, paths := tree(t)
	write(t, paths.XDG, "[budget]\nmonth = 2000\n"+
		block("github.com/org/repo", "[budget]\nday = 220\nweek = 800.5\n"))
	cfg, err := Load(root, paths)
	if err != nil || cfg.Budget != (Budget{Day: 220, Week: 800.5, Month: 2000}) {
		t.Errorf("budget = %+v, %v", cfg.Budget, err)
	}
}

// TestCoversIsByPrefix pins which repos a block covers.
func TestCoversIsByPrefix(t *testing.T) {
	for _, tt := range []struct {
		prefix, key string
		want        bool
	}{
		{"github.com/org", "github.com/org/repo", true},
		{"github.com/org/repo", "github.com/org/repo", true},
		{"github.com/org/", "github.com/org/repo", true},
		{"github.com/org/rep", "github.com/org/repo", false},
		{"github.com/other", "github.com/org/repo", false},
	} {
		if got := covers(tt.prefix, tt.key); got != tt.want {
			t.Errorf("covers(%q, %q) = %v", tt.prefix, tt.key, got)
		}
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
	write(t, paths.XDG, block("github.com/org/repo", "land = \"merge\"\n"))
	if cfg, err := Load(root, paths); err != nil || cfg.Land != LandMerge {
		t.Errorf("land = %+v, %v", cfg, err)
	}
	write(t, paths.XDG, block("github.com/org/repo", "land = \"push\"\n"))
	if _, err := Load(root, paths); err == nil || !strings.Contains(err.Error(), "land") {
		t.Errorf("land = push loaded: %v", err)
	}
}
