package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
)

// ensureGate checks the repo's config before diatom starts on it. Without a
// gate nothing can be committed, so it asks for one on a terminal and saves
// the answer in the repo's config; anywhere else it refuses to start. ask
// says whether in is a terminal someone can answer at.
func ensureGate(s *queue.Store, paths config.Paths, in io.Reader, out io.Writer, ask bool) error {
	cfg, err := config.Load(s.Repo(), paths)
	if err != nil || strings.TrimSpace(cfg.Gate) != "" {
		return err
	}
	name := filepath.Base(s.Repo())
	path := filepath.Join(paths.XDG, config.FileName)
	kind := "go"
	if kinds := config.Kinds(s.Repo()); len(kinds) > 0 {
		kind = kinds[0]
	}
	hint := fmt.Sprintf("set gate in the repo's block of %s, or gates.%s there to give every "+
		"%s repo one", path, kind, kind)
	if !ask {
		return fmt.Errorf("%s has no gate, the command every commit must pass: %s", name, hint)
	}
	_, _ = fmt.Fprintf(out, "%s has no gate, the command every commit must pass, such as "+
		"`mage ci:check`.\nIt will be saved in %s. (Or quit and %s.)\nGate: ", name, path, hint)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	gate := strings.TrimSpace(line)
	if gate == "" {
		return fmt.Errorf("no gate given: %s", hint)
	}
	if err := config.Set(paths, paths.Key, map[string]any{"gate": gate}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Saved `%s` as %s's gate.\n", gate, name)
	return nil
}

// terminal reports whether in is a terminal someone can answer at.
func terminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
