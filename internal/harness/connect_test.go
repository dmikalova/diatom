package harness

import (
	"slices"
	"testing"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestPlanningCarriesTheTracker(t *testing.T) {
	cfg := &config.Config{
		Tickets:       "linear",
		MaxConnectors: 2,
		MCPServers: map[string]any{
			"linear":  map[string]any{"command": "linear-mcp"},
			"metrics": map[string]any{"command": "dd-mcp"},
			"docs":    map[string]any{"command": "docs-mcp"},
		}}
	tasks := []*queue.Task{{MCPServers: []string{"docs", "metrics"}}}

	if got := attached(cfg, tasks, false); !slices.Equal(got, []string{"docs", "metrics"}) {
		t.Errorf("a working session's connectors = %v, want only its tasks' own", got)
	}
	// The tracker comes first, so the cap never drops the one connector the
	// session needs to hand a goal in.
	if got := attached(cfg, tasks, true); !slices.Equal(got, []string{"linear", "docs"}) {
		t.Errorf("a planning session's connectors = %v, want the tracker first", got)
	}

	cfg.Tickets = ""
	if got := attached(cfg, nil, true); len(got) != 0 {
		t.Errorf("connectors without a tracker = %v, want none", got)
	}
	cfg.Tickets = "jira"
	if got := attached(cfg, nil, true); len(got) != 0 {
		t.Errorf("connectors for a tracker the catalog has no server for = %v, want none", got)
	}
}
