package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/config"
)

func TestUndecodedKeysReportsMisplacedAndUnknown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	// api_key at the top level is misplaced (it belongs under [provider]); bogus
	// is not a key lore knows. Both must be reported so a user who edits the file
	// and re-runs into the same failure gets told why.
	body := "api_key = \"x\"\n\n[provider]\nbase_url = \"http://h/v1\"\nbogus = \"y\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := config.UndecodedKeys(p)
	joined := strings.Join(keys, ",")
	if !strings.Contains(joined, "api_key") || !strings.Contains(joined, "bogus") {
		t.Fatalf("want api_key and provider.bogus flagged, got %v", keys)
	}
}

func TestUndecodedKeysCleanConfigIsNil(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := "[provider]\nbase_url = \"http://h/v1\"\napi_key = \"x\"\nembed_model = \"m\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if keys := config.UndecodedKeys(p); len(keys) != 0 {
		t.Fatalf("clean config should have no unknown keys, got %v", keys)
	}
}

func TestUndecodedKeysAbsentFileIsNil(t *testing.T) {
	if keys := config.UndecodedKeys(filepath.Join(t.TempDir(), "absent.toml")); keys != nil {
		t.Fatalf("absent file: want nil, got %v", keys)
	}
}
