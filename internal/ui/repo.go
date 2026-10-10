package ui

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
	"github.com/dmikalova/diatom/internal/reviewui"
	"github.com/dmikalova/diatom/internal/tui"
)

// repoPage is a repo's config as the window sets it (ADR 0013): every lever
// a repo may set, what it is now, and where that came from. Setting one
// writes it into the repo's block of the one config file; clearing one falls
// it back to the level above.
//
// Only the scalars are here. A map or a list reads better in the file, so
// the page offers to open the file instead.
type repoPage struct {
	store *queue.Store
	// sel is the setting the page is on, and editing is set while its value
	// is typed; typed is what is typed so far.
	sel     int
	editing bool
	typed   string
	err     error
	flash   string
}

// repoPageFor opens the page on a repo, keeping where it was if it is the
// same repo.
func (a *App) repoPageFor(store *queue.Store) *repoPage {
	if a.repo != nil && a.repo.store == store {
		return a.repo
	}
	a.repo = &repoPage{store: store}
	return a.repo
}

// settings are the levers the page lists: the repo's, in the config's order.
func settings() []config.Setting {
	var out []config.Setting
	for _, s := range config.Settings {
		if s.Repo() {
			out = append(out, s)
		}
	}
	return out
}

// renderRepo draws the page.
func (a *App) renderRepo(e entry, width int) string {
	p := a.repoPageFor(e.row.store)
	cfg, err := a.env.configFor(p.store)
	if err != nil {
		return tui.Color("The config won't load: "+err.Error(), tui.Red)
	}
	set := p.set()
	var b strings.Builder
	key := p.store.Key()
	if key == "" {
		key = p.store.Repo()
	}
	b.WriteString(boldAll(key) + "\n")
	b.WriteString(tui.Dim("What this repo is run by. A lever set here is written into the "+
		"config's block for it; one left alone takes what the level above says.") + "\n\n")
	for i, s := range settings() {
		b.WriteString(p.line(i, s, cfg, set, width) + "\n")
	}
	b.WriteString("\n" + tui.Dim("enter sets · x clears it · o opens the config file, where the "+
		"lists and the tables are") + "\n")
	switch {
	case p.err != nil:
		b.WriteString(tui.Color(p.err.Error(), tui.Red) + "\n")
	case p.flash != "":
		b.WriteString(tui.Color(p.flash, tui.Green) + "\n")
	}
	return b.String()
}

// line is one setting: its name, what it is now, and where that came from.
func (p *repoPage) line(i int, s config.Setting, cfg *config.Config,
	set map[string]any, width int) string {
	lead := "  "
	name := s.Key
	if i == p.sel {
		lead, name = tui.Color("▌ ", tui.Accent), boldAll(s.Key)
	}
	value := settingValue(cfg, s.Key)
	from := tui.Dim("inherited")
	if _, ok := set[s.Key]; ok {
		from = tui.Color("set here", tui.Green)
	}
	if i == p.sel && p.editing {
		return lead + name + ": " + tui.Color(p.typed+"▏", tui.Accent) + "\n" +
			"    " + tui.Dim(s.What)
	}
	if value == "" {
		value = tui.Dim("unset")
	}
	head := lead + name + ": " + value + "  " + from
	if i != p.sel {
		return ansi.Truncate(head, max(width-1, 10), "…")
	}
	return head + "\n" + "    " + tui.Dim(s.What)
}

// set is what the repo's own block sets, so the page can say which levers
// are its own and which it takes from above.
func (p *repoPage) set() map[string]any {
	if p.store.Key() == "" {
		return nil
	}
	own, err := config.Own(config.Paths{}, p.store.Key())
	if err != nil {
		return nil
	}
	return own
}

// settingValue is what the loaded config says a key is, as text.
func settingValue(cfg *config.Config, key string) string {
	v, ok := cfg.Value(key)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case time.Duration:
		if t == 0 {
			return ""
		}
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// repoKey hands a key to the page. It reports whether the key was the nav's
// instead, which moves off the page, and any command the key ran.
func (a *App) repoKey(e entry, k string) (bool, tea.Cmd) {
	p := a.repoPageFor(e.row.store)
	list := settings()
	if p.editing {
		switch k {
		case "esc":
			p.editing, p.typed = false, ""
		case "enter":
			p.write(list[p.sel], p.typed)
			p.editing, p.typed = false, ""
		case "backspace":
			if p.typed != "" {
				p.typed = p.typed[:len(p.typed)-1]
			}
		default:
			if len(k) == 1 || k == " " {
				p.typed += k
			}
		}
		return false, nil
	}
	switch k {
	case "esc", "left":
		return true, nil
	case "down", "j":
		p.sel = min(p.sel+1, len(list)-1)
	case "up", "k":
		p.sel = max(p.sel-1, 0)
	case "enter":
		p.editing, p.typed, p.err, p.flash = true, settingValue(
			a.cfgOf(p),
			list[p.sel].Key,
		), nil, ""
		if list[p.sel].Kind == config.Flag {
			p.editing = false
			p.write(list[p.sel], flip(p.typed))
			p.typed = ""
		}
	case "x":
		p.write(list[p.sel], "")
	case openKey:
		return false, a.repoOpen(e)
	}
	return false, nil
}

// cfgOf is the repo's loaded config, or an empty one when it won't load.
func (a *App) cfgOf(p *repoPage) *config.Config {
	cfg, err := a.env.configFor(p.store)
	if err != nil {
		return &config.Config{}
	}
	return cfg
}

// flip turns a flag over.
func flip(v string) string {
	if v == "true" {
		return "false"
	}
	return "true"
}

// write saves one lever into the repo's block, or clears it when value is
// empty.
func (p *repoPage) write(s config.Setting, value string) {
	p.err, p.flash = nil, ""
	if p.store.Key() == "" {
		p.err = fmt.Errorf("diatom has no name for this repo, so it has no block to set")
		return
	}
	var v any
	switch {
	case value == "":
		v = nil
	case s.Kind == config.Number:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			p.err = fmt.Errorf("%s takes a whole number", s.Key)
			return
		}
		v = n
	case s.Kind == config.Flag:
		v = value == "true"
	case s.Kind == config.Duration:
		if _, err := time.ParseDuration(strings.TrimSpace(value)); err != nil {
			p.err = fmt.Errorf("%s takes a length of time, such as 10m", s.Key)
			return
		}
		v = strings.TrimSpace(value)
	default:
		v = value
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		p.err = err
		return
	}
	if err := config.Set(paths, p.store.Key(), map[string]any{s.Key: v}); err != nil {
		p.err = err
		return
	}
	if v == nil {
		p.flash = s.Key + " is cleared. It takes effect when diatom next starts."
		return
	}
	p.flash = s.Key + " is saved. It takes effect when diatom next starts."
}

// repoOpen opens the config file in the editor, where the lists and the
// tables the page does not set are edited.
func (a *App) repoOpen(e entry) tea.Cmd {
	p := a.repoPageFor(e.row.store)
	paths, err := config.DefaultPaths()
	if err != nil {
		p.err = err
		return nil
	}
	cfg, err := a.env.configFor(p.store)
	if err != nil {
		p.err = err
		return nil
	}
	editor := strings.Fields(cfg.Editor)
	if len(editor) == 0 {
		p.flash = "no editor is set in the config"
		return nil
	}
	cmd := exec.CommandContext(a.ctx, editor[0], append(editor[1:], config.File(paths))...)
	cmd.Dir = p.store.Repo()
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return reviewui.EditedMsg{Err: err} })
}
