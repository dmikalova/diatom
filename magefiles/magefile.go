//go:build mage

// diatom developer tasks, all from project-standards' shared ci package. Run
// `mage -l` to list them, and `mage ci:fix && mage ci:check` before calling
// work done.
package main

import (
	// mage:import ci
	_ "github.com/dmikalova/project-standards/ci"
)
