package config_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/config"
	"github.com/jmurray2011/lore/internal/domain"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load("", env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != config.Defaults() {
		t.Errorf("Load() = %+v, want defaults %+v", cfg, config.Defaults())
	}
}

func TestLoadEnvOverridesDefaults(t *testing.T) {
	cfg, err := config.Load("", env(map[string]string{
		"LORE_BASE_URL":    "http://localhost:11434/v1",
		"LORE_EMBED_MODEL": "nomic-embed-text",
		"LORE_DIMENSIONS":  "768",
		"LORE_LOG_LEVEL":   "debug",
		"LORE_LOG_FORMAT":  "json",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("base url = %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.EmbedModel != "nomic-embed-text" {
		t.Errorf("embed model = %q", cfg.Provider.EmbedModel)
	}
	if cfg.Provider.Dimensions != 768 {
		t.Errorf("dimensions = %d", cfg.Provider.Dimensions)
	}
	if cfg.Log.Level != slog.LevelDebug || cfg.Log.Format != "json" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.Provider.ChatModel != config.Defaults().Provider.ChatModel {
		t.Errorf("untouched default changed: %q", cfg.Provider.ChatModel)
	}
}

func TestLoadFileThenEnvPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[provider]
base_url = "http://file-url/v1"
embed_model = "file-embed"
dimensions = 256
chat_model = "file-chat"

[log]
level = "warn"
format = "json"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path, env(map[string]string{"LORE_BASE_URL": "http://env-url/v1"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.BaseURL != "http://env-url/v1" {
		t.Errorf("env must beat file: %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.EmbedModel != "file-embed" || cfg.Provider.Dimensions != 256 {
		t.Errorf("file values lost: %+v", cfg.Provider)
	}
	if cfg.Log.Level != slog.LevelWarn || cfg.Log.Format != "json" {
		t.Errorf("file log not applied: %+v", cfg.Log)
	}
}

func TestLoadStructuredOutputCapability(t *testing.T) {
	if config.Defaults().Provider.StructuredOutput {
		t.Error("structured output must default to off (works against any OpenAI-compatible endpoint)")
	}

	cfg, err := config.Load("", env(map[string]string{"LORE_STRUCTURED_OUTPUT": "true"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Provider.StructuredOutput {
		t.Error("LORE_STRUCTURED_OUTPUT=true did not enable the capability")
	}
}

func TestLoadIngestConcurrency(t *testing.T) {
	if got := config.Defaults().Ingest.Concurrency; got != 0 {
		t.Errorf("default concurrency = %d, want 0 (use built-in default)", got)
	}

	cfg, err := config.Load("", env(map[string]string{"LORE_INGEST_CONCURRENCY": "2"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ingest.Concurrency != 2 {
		t.Errorf("LORE_INGEST_CONCURRENCY not applied: %d", cfg.Ingest.Concurrency)
	}

	for _, bad := range []string{"lots", "-1"} {
		if _, err := config.Load("", env(map[string]string{"LORE_INGEST_CONCURRENCY": bad})); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("LORE_INGEST_CONCURRENCY=%q: want ErrInvalidArgument, got %v", bad, err)
		}
	}
}

func TestLoadAuth(t *testing.T) {
	if got := config.Defaults().Provider.Auth; got != "bearer" {
		t.Errorf("default auth = %q, want bearer", got)
	}

	cfg, err := config.Load("", env(map[string]string{"LORE_AUTH": "api-key"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Auth != "api-key" {
		t.Errorf("LORE_AUTH not applied: %q", cfg.Provider.Auth)
	}

	if _, err := config.Load("", env(map[string]string{"LORE_AUTH": "oauth"})); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("invalid auth: want ErrInvalidArgument, got %v", err)
	}
}

func TestLoadAttachmentCapabilities(t *testing.T) {
	d := config.Defaults().Provider
	if d.ImageInput || d.DocumentInput {
		t.Error("attachment capabilities must default to off")
	}

	cfg, err := config.Load("", env(map[string]string{
		"LORE_IMAGE_INPUT":    "true",
		"LORE_DOCUMENT_INPUT": "true",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Provider.ImageInput || !cfg.Provider.DocumentInput {
		t.Errorf("env did not enable attachment capabilities: %+v", cfg.Provider)
	}
}

func TestLoadStorageDefaults(t *testing.T) {
	cfg, err := config.Load("", env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Storage{Backend: "sqlite", Path: ""}
	if cfg.Storage != want {
		t.Errorf("storage defaults = %+v, want %+v", cfg.Storage, want)
	}
}

func TestLoadStorageFileThenEnvPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[storage]
backend = "memory"
path = "/file/lore.db"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path, env(map[string]string{"LORE_DB_PATH": "/env/lore.db"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.Backend != "memory" {
		t.Errorf("file backend lost: %q", cfg.Storage.Backend)
	}
	if cfg.Storage.Path != "/env/lore.db" {
		t.Errorf("env must beat file: %q", cfg.Storage.Path)
	}
}

func TestDefaultDBPath(t *testing.T) {
	p, err := config.DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("lore", "lore.db")) {
		t.Errorf("DefaultDBPath = %q, want suffix lore/lore.db", p)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"), env(nil))
	if err != nil {
		t.Fatalf("missing file should be fine: %v", err)
	}
	if cfg != config.Defaults() {
		t.Errorf("want defaults, got %+v", cfg)
	}
}

func TestLoadInvalidIsErrInvalidArgument(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"bad dimensions", map[string]string{"LORE_DIMENSIONS": "lots"}},
		{"bad level", map[string]string{"LORE_LOG_LEVEL": "loud"}},
		{"bad format", map[string]string{"LORE_LOG_FORMAT": "yaml"}},
		{"bad backend", map[string]string{"LORE_STORAGE_BACKEND": "lmdb"}},
	}
	for _, c := range cases {
		if _, err := config.Load("", env(c.env)); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: want ErrInvalidArgument, got %v", c.name, err)
		}
	}
}

func TestTimeoutDefaultAndOverride(t *testing.T) {
	cfg, err := config.Load("", env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Timeout != 120*time.Second {
		t.Errorf("default timeout = %v, want 120s", cfg.Provider.Timeout)
	}

	cfg, err = config.Load("", env(map[string]string{"LORE_TIMEOUT": "30s"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Timeout != 30*time.Second {
		t.Errorf("env timeout = %v, want 30s", cfg.Provider.Timeout)
	}

	// "0" disables the per-request timeout (escape hatch).
	cfg, err = config.Load("", env(map[string]string{"LORE_TIMEOUT": "0"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Timeout != 0 {
		t.Errorf("zero timeout = %v, want 0 (disabled)", cfg.Provider.Timeout)
	}
}

func TestTimeoutInvalidIsErrInvalidArgument(t *testing.T) {
	for _, v := range []string{"soon", "-5s"} {
		if _, err := config.Load("", env(map[string]string{"LORE_TIMEOUT": v})); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("LORE_TIMEOUT=%q: want ErrInvalidArgument, got %v", v, err)
		}
	}
}

func TestResolveFlagsOutrankEnvAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[log]
level = "warn"
format = "json"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// File says warn/json, env says debug/json, flags say error/text.
	// Flags rank highest, so the result must be error/text.
	flags := config.FlagOverrides{LogLevel: "error", LogFormat: "text"}
	cfg, err := config.Resolve(path, env(map[string]string{
		"LORE_LOG_LEVEL":  "debug",
		"LORE_LOG_FORMAT": "json",
	}), flags)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Log.Level != slog.LevelError {
		t.Errorf("flag level must beat env and file: got %v", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("flag format must beat env and file: got %q", cfg.Log.Format)
	}
}

func TestResolveVerboseForcesDebug(t *testing.T) {
	cfg, err := config.Resolve("", env(nil), config.FlagOverrides{Verbose: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Log.Level != slog.LevelDebug {
		t.Errorf("--verbose should force debug, got %v", cfg.Log.Level)
	}
}

func TestResolveExplicitLevelBeatsVerbose(t *testing.T) {
	cfg, err := config.Resolve("", env(nil), config.FlagOverrides{LogLevel: "warn", Verbose: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Log.Level != slog.LevelWarn {
		t.Errorf("explicit --log-level should beat --verbose, got %v", cfg.Log.Level)
	}
}

func TestResolveInvalidFlagIsErrInvalidArgument(t *testing.T) {
	cases := []struct {
		name  string
		flags config.FlagOverrides
	}{
		{"bad level", config.FlagOverrides{LogLevel: "loud"}},
		{"bad format", config.FlagOverrides{LogFormat: "yaml"}},
	}
	for _, c := range cases {
		if _, err := config.Resolve("", env(nil), c.flags); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: want ErrInvalidArgument, got %v", c.name, err)
		}
	}
}

func TestLoadIsResolveWithoutFlags(t *testing.T) {
	// Load must remain equivalent to Resolve with empty overrides so existing
	// callers and tests are unaffected.
	got, err := config.Load("", env(map[string]string{"LORE_LOG_LEVEL": "warn"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, err := config.Resolve("", env(map[string]string{"LORE_LOG_LEVEL": "warn"}), config.FlagOverrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Load %+v != Resolve %+v", got, want)
	}
}

func TestNewLogger(t *testing.T) {
	t.Run("json handler respects level", func(t *testing.T) {
		var buf bytes.Buffer
		lg := config.NewLogger(config.Log{Level: slog.LevelWarn, Format: "json"}, &buf)
		lg.Info("below-threshold")
		lg.Warn("at-threshold")

		out := buf.String()
		if strings.Contains(out, "below-threshold") {
			t.Error("info must be filtered at warn level")
		}
		if !strings.Contains(out, `"msg":"at-threshold"`) {
			t.Errorf("want JSON warn line, got %q", out)
		}
	})

	t.Run("text handler", func(t *testing.T) {
		var buf bytes.Buffer
		lg := config.NewLogger(config.Log{Level: slog.LevelInfo, Format: "text"}, &buf)
		lg.Info("hello")

		out := buf.String()
		if !strings.Contains(out, "hello") || strings.Contains(out, `"msg"`) {
			t.Errorf("want text output with hello, got %q", out)
		}
	})
}
