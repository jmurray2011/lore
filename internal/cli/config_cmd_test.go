package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/config"
)

func TestConfigPathHonorsDefaultAndFlag(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})

	out, code := exec(deps, "config", "path")
	if code != 0 || !strings.Contains(out, "config.toml") {
		t.Fatalf("config path (default): code=%d out=%q", code, out)
	}
	out, code = exec(deps, "--config", "/custom/place.toml", "config", "path")
	if code != 0 || !strings.Contains(out, "/custom/place.toml") {
		t.Fatalf("config path (flag): code=%d out=%q", code, out)
	}
}

func TestConfigInitWritesLoadableStarterAndRefusesOverwrite(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	// A nested path proves parent directories are created.
	target := filepath.Join(t.TempDir(), "sub", "config.toml")

	if _, code := exec(deps, "config", "init", target); code != 0 {
		t.Fatalf("config init exit %d", code)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("starter config not written: %v", err)
	}
	for _, want := range []string{"[provider]", "base_url", "embed_model", "chat_model"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("starter config missing %q", want)
		}
	}
	// The starter must load clean through the real resolver — valid TOML, and only
	// keys lore recognizes (else `config init` would emit a file that immediately
	// warns "unrecognized keys").
	if keys := config.UndecodedKeys(target); len(keys) != 0 {
		t.Errorf("starter config has unrecognized keys: %v", keys)
	}

	// A second init must refuse to clobber the existing file.
	if _, code := exec(deps, "config", "init", target); code == 0 {
		t.Error("config init should refuse to overwrite an existing file")
	}
}
