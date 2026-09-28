package ui

import (
	"strings"
	"testing"

	"github.com/dmikalova/diatom/internal/config"
	"github.com/dmikalova/diatom/internal/queue"
)

func TestLandActionsFollowTheConfig(t *testing.T) {
	keys := func(land string) string {
		var out []string
		for _, a := range landActions(&goalRow{goal: &queue.Goal{Base: "main"}, land: land}) {
			out = append(out, a.key)
		}
		return strings.Join(out, " ")
	}
	if keys("") != "P F" || keys(config.LandMerge) != "P" || keys(config.LandPRs) != "F" {
		t.Errorf("land actions: %q, %q, %q", keys(""), keys(config.LandMerge), keys(config.LandPRs))
	}
	s := &Status{}
	row := &goalRow{goal: &queue.Goal{Name: "g", Base: "main"}, land: config.LandMerge}
	if s.landsBy(row, "F") || !strings.Contains(s.flash, "P merges") || !s.landsBy(row, "P") {
		t.Errorf("landsBy with land = merge: %q", s.flash)
	}
}
