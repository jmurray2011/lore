// Package config loads lore's typed configuration with the precedence
// flags > env (LORE_*) > file (TOML) > defaults, and builds the slog logger.
// It is loaded once in the composition root and passed down as values; nothing
// reads configuration globally (DESIGN.md).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jmurray2011/lore/internal/domain"
)

// Config is lore's resolved configuration.
type Config struct {
	Provider Provider
	Storage  Storage
	Log      Log
}

// Storage selects the persistence backend. Backend is "sqlite" (default) or
// "memory". Path is the SQLite database file; empty means the default location
// (DefaultDBPath) and it is ignored for the memory backend.
type Storage struct {
	Backend string
	Path    string
}

// Provider configures the OpenAI-compatible backend.
type Provider struct {
	BaseURL    string
	APIKey     string
	EmbedModel string
	Dimensions int
	ChatModel  string
	// StructuredOutput declares that the provider supports JSON-schema
	// (response_format) output. Off by default so lore works against any
	// OpenAI-compatible endpoint; enable it for providers that support it
	// (decision 19).
	StructuredOutput bool
}

// Log configures the slog logger.
type Log struct {
	Level  slog.Level
	Format string // "text" or "json"
}

// Defaults returns the baseline configuration before file and environment
// overlays. The provider defaults target OpenAI; point BaseURL elsewhere for
// Ollama, vLLM, LM Studio, etc.
func Defaults() Config {
	return Config{
		Provider: Provider{
			BaseURL:    "https://api.openai.com/v1",
			EmbedModel: "text-embedding-3-small",
			Dimensions: 1536,
			ChatModel:  "gpt-4o-mini",
		},
		Storage: Storage{Backend: "sqlite"},
		Log:     Log{Level: slog.LevelInfo, Format: "text"},
	}
}

// Load builds a Config from defaults, then the TOML file at path (skipped when
// path is empty or the file is absent), then LORE_* variables read via getenv.
// Flag overrides, which rank highest, are applied by the caller.
func Load(path string, getenv func(string) string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		var fc fileConfig
		switch _, err := toml.DecodeFile(path, &fc); {
		case err == nil:
			if err := applyFile(&cfg, fc); err != nil {
				return Config{}, err
			}
		case errors.Is(err, fs.ErrNotExist):
			// absent file: defaults + env only
		default:
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
	}

	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type fileConfig struct {
	Provider struct {
		BaseURL          string `toml:"base_url"`
		APIKey           string `toml:"api_key"`
		EmbedModel       string `toml:"embed_model"`
		Dimensions       int    `toml:"dimensions"`
		ChatModel        string `toml:"chat_model"`
		StructuredOutput bool   `toml:"structured_output"`
	} `toml:"provider"`
	Storage struct {
		Backend string `toml:"backend"`
		Path    string `toml:"path"`
	} `toml:"storage"`
	Log struct {
		Level  string `toml:"level"`
		Format string `toml:"format"`
	} `toml:"log"`
}

func applyFile(cfg *Config, fc fileConfig) error {
	setString(&cfg.Provider.BaseURL, fc.Provider.BaseURL)
	setString(&cfg.Provider.APIKey, fc.Provider.APIKey)
	setString(&cfg.Provider.EmbedModel, fc.Provider.EmbedModel)
	setString(&cfg.Provider.ChatModel, fc.Provider.ChatModel)
	setString(&cfg.Storage.Backend, fc.Storage.Backend)
	setString(&cfg.Storage.Path, fc.Storage.Path)
	setString(&cfg.Log.Format, fc.Log.Format)
	if fc.Provider.Dimensions != 0 {
		cfg.Provider.Dimensions = fc.Provider.Dimensions
	}
	if fc.Provider.StructuredOutput {
		cfg.Provider.StructuredOutput = true
	}
	if fc.Log.Level != "" {
		lvl, err := parseLevel(fc.Log.Level)
		if err != nil {
			return err
		}
		cfg.Log.Level = lvl
	}
	return nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	setString(&cfg.Provider.BaseURL, getenv("LORE_BASE_URL"))
	setString(&cfg.Provider.APIKey, getenv("LORE_API_KEY"))
	setString(&cfg.Provider.EmbedModel, getenv("LORE_EMBED_MODEL"))
	setString(&cfg.Provider.ChatModel, getenv("LORE_CHAT_MODEL"))
	setString(&cfg.Storage.Backend, getenv("LORE_STORAGE_BACKEND"))
	setString(&cfg.Storage.Path, getenv("LORE_DB_PATH"))
	setString(&cfg.Log.Format, getenv("LORE_LOG_FORMAT"))

	if v := getenv("LORE_DIMENSIONS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("config: %w: LORE_DIMENSIONS %q is not an integer", domain.ErrInvalidArgument, v)
		}
		cfg.Provider.Dimensions = d
	}
	if v := getenv("LORE_STRUCTURED_OUTPUT"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: %w: LORE_STRUCTURED_OUTPUT %q is not a boolean", domain.ErrInvalidArgument, v)
		}
		cfg.Provider.StructuredOutput = b
	}
	if v := getenv("LORE_LOG_LEVEL"); v != "" {
		lvl, err := parseLevel(v)
		if err != nil {
			return err
		}
		cfg.Log.Level = lvl
	}
	return nil
}

func setString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: %w: unknown log level %q", domain.ErrInvalidArgument, s)
	}
}

func validate(cfg Config) error {
	if cfg.Log.Format != "text" && cfg.Log.Format != "json" {
		return fmt.Errorf("config: %w: log format %q (want \"text\" or \"json\")", domain.ErrInvalidArgument, cfg.Log.Format)
	}
	if cfg.Provider.Dimensions <= 0 {
		return fmt.Errorf("config: %w: provider dimensions must be positive, got %d", domain.ErrInvalidArgument, cfg.Provider.Dimensions)
	}
	if cfg.Storage.Backend != "sqlite" && cfg.Storage.Backend != "memory" {
		return fmt.Errorf("config: %w: storage backend %q (want \"sqlite\" or \"memory\")", domain.ErrInvalidArgument, cfg.Storage.Backend)
	}
	return nil
}

// DefaultPath is the conventional config location:
// <user-config-dir>/lore/config.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: locate user config dir: %w", err)
	}
	return filepath.Join(dir, "lore", "config.toml"), nil
}

// DefaultDBPath is the conventional SQLite database location, alongside the
// config file: <user-config-dir>/lore/lore.db.
func DefaultDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: locate user config dir: %w", err)
	}
	return filepath.Join(dir, "lore", "lore.db"), nil
}
