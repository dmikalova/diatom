package panes

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/dmikalova/diatom/internal/config"
)

// The nav's width: as it starts, and the least it can be dragged to. It can
// be dragged to half the window at most.
const (
	navDefault = 32
	navLeast   = 20
)

// uiState is how the human left the window, kept for every repo.
type uiState struct {
	NavWidth  int  `toml:"navWidth"`
	NavHidden bool `toml:"navHidden"`
}

// uiStatePath is where the window's state is kept: the XDG state directory.
func uiStatePath(p config.Paths) string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(p.Home, ".local", "state")
	}
	return filepath.Join(dir, "diatom", "ui.toml")
}

// loadUI reads how the window was left; with nothing kept, it starts as new.
func (a *App) loadUI() {
	a.navW = navDefault
	var st uiState
	if _, err := toml.DecodeFile(uiStatePath(a.env.Paths), &st); err != nil {
		return
	}
	if st.NavWidth > 0 {
		a.navW = max(st.NavWidth, navLeast)
	}
	a.navHidden = st.NavHidden
}

// saveUI keeps how the window is now. Failing to is no reason to stop.
func (a *App) saveUI() {
	path := uiStatePath(a.env.Paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = toml.NewEncoder(f).Encode(uiState{NavWidth: a.navW, NavHidden: a.navHidden})
}

// nw is the nav's width on screen: none while it is hidden, and never more
// than half the window.
func (a *App) nw() int {
	if a.navHidden {
		return 0
	}
	return max(min(a.navW, a.width/2), navLeast)
}

// toggleNav hides the nav, or shows it again.
func (a *App) toggleNav() {
	a.navHidden = !a.navHidden
	if a.navHidden && a.focus != partMain {
		a.focus = partMain
	}
	a.saveUI()
	a.layout()
}

// drag moves the nav's edge to column x while it is held.
func (a *App) drag(x int) {
	a.navW = max(min(x, a.width/2), navLeast)
	a.layout()
}
