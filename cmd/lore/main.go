// Command lore is the composition root: it wires config, logging, adapters,
// and use cases together, and delegates everything else to internal/cli.
//
// The CLI surface is not implemented yet — see DESIGN.md and the next-steps
// list in CLAUDE.md. This stub exists so the build/release pipeline is real
// from day one (version embedding via ldflags, goreleaser, CI build).
package main

import (
	"fmt"
	"os"
)

// Set by goreleaser via -ldflags (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		// stdout is data (DESIGN.md): version goes to stdout.
		fmt.Printf("lore %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	fmt.Fprintln(os.Stderr, "lore: no commands implemented yet — see DESIGN.md")
	os.Exit(2) // usage error, per DESIGN.md exit codes
}
