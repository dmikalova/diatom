package harness

import (
	"slices"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/session"
)

// attached is the connectors a batch carries: the union of its tasks' own,
// capped by the config (ADR 0013). Splitting a batch by connector would cost
// more sessions than the context the union costs, but an uncapped union puts
// every server of a mixed batch in one session, which is what the catalog
// exists to avoid.
func attached(cfg *config.Config, tasks []*queue.Task) []string {
	var names []string
	for _, t := range tasks {
		for _, name := range t.MCPServers {
			if _, ok := cfg.MCPServers[name]; ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	if cfg.MaxConnectors > 0 && len(names) > cfg.MaxConnectors {
		names = names[:cfg.MaxConnectors]
	}
	return names
}

// catalog is the names of the repo's connectors, which bound what a session
// may ask for.
func catalog(cfg *config.Config) []string {
	var names []string
	for _, c := range cfg.Connectors() {
		names = append(names, c.Name)
	}
	return names
}

// applyConnects records the connectors a session asked for on the tasks that
// asked, so the next session of that task carries them. A task that asked is
// not done, so it is already going back to the queue.
func (h *Harness) applyConnects(repo Repo, goal string, report session.Report) error {
	byTask := map[string][]string{}
	for _, e := range report.Connects {
		if _, ok := repo.Config.MCPServers[e.Server]; ok {
			byTask[e.Task] = append(byTask[e.Task], e.Server)
		}
	}
	for id, servers := range byTask {
		t, err := repo.Store.Task(goal, id)
		if err != nil {
			return err
		}
		var added []string
		for _, name := range servers {
			if !slices.Contains(t.MCPServers, name) {
				t.MCPServers = append(t.MCPServers, name)
				added = append(added, name)
			}
		}
		if len(added) == 0 {
			continue
		}
		if err := repo.Store.SaveTask(goal, t); err != nil {
			return err
		}
		h.log().Info("connector added to task", "goal", goal, "task", id, "connectors", added)
	}
	return nil
}
