package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestExplicitMissingConfigIsUsageError proves that pointing --config at a file
// that does not exist fails loudly (exit 2) instead of silently falling back to
// defaults — the silent fallback let a user run for a long time against the
// wrong endpoint/DB believing their config was loaded.
func TestExplicitMissingConfigIsUsageError(t *testing.T) {
	// Hermetic: even if the guard regresses and the command proceeds, memory
	// storage and an isolated config dir keep it off the real DB.
	t.Setenv("LORE_STORAGE_BACKEND", "memory")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.toml")
	code := run(context.Background(), []string{"--config", missing, "ls"}, &out, &errb)

	if code != 2 {
		t.Fatalf("want exit 2 for missing --config, got %d (stderr=%s)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "does not exist") {
		t.Fatalf("stderr should explain the missing config file, got: %s", errb.String())
	}
}
