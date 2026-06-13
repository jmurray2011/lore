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
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jmurray2011/lore/internal/domain"
)

// Config is lore's resolved configuration.
type Config struct {
	Provider Provider
	Rerank   Rerank
	Storage  Storage
	Ingest   Ingest
	Cache    Cache
	Log      Log
}

// Rerank configures the cross-encoder rerank provider for two-stage retrieval.
// It is deliberately separate from Provider: rerank APIs are Cohere-style, not
// OpenAI-compatible, and are commonly a different vendor (embed with OpenAI,
// rerank with Cohere/Jina). Empty BaseURL/Model means reranking is unconfigured;
// requesting it then is a usage error.
type Rerank struct {
	BaseURL string
	APIKey  string
	Auth    string // "bearer" (default) or "api-key"
	Model   string
	Timeout time.Duration
}

// Cache configures the answer cache: reuse of synthesized ask/synthesize answers
// across runs. Off by default (opt-in); the cache self-invalidates when the
// question, grounding text, or model/prompt change, and TTL bounds entry age.
type Cache struct {
	Enabled bool
	// TTL is the maximum age of a reusable cached answer. Must be positive to be
	// useful (a non-positive TTL means every entry reads as expired).
	TTL time.Duration
}

// Ingest tunes the ingestion pipeline.
type Ingest struct {
	// Concurrency bounds parallel embedding during ingest. 0 means use the
	// built-in default; lower it for providers with tight rate limits.
	Concurrency int
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
	// Auth selects how the API key is sent: "bearer" (OpenAI default) or
	// "api-key" (Azure OpenAI's header, decision 21).
	Auth string
	// Timeout bounds each HTTP request to the provider (per attempt, so retries
	// each get the full budget). Zero disables it. Guards against a hung
	// provider blocking forever in non-interactive use (CI, scripts), where
	// there is no SIGINT to cancel the request context (decision 36).
	Timeout time.Duration
	// StructuredOutput declares that the provider supports JSON-schema
	// (response_format) output. Off by default so lore works against any
	// OpenAI-compatible endpoint; enable it for providers that support it
	// (decision 19).
	StructuredOutput bool
	// ImageInput / DocumentInput declare that the provider accepts image /
	// document attachments (decision 20). Off by default; `ask --attach`
	// errors for an attachment whose capability is off.
	ImageInput    bool
	DocumentInput bool
}

// Log configures the slog logger.
type Log struct {
	Level  slog.Level
	Format string // "text" or "json"
}

// DefaultProviderTimeout bounds each provider HTTP request unless overridden.
// Generous enough not to cut off long generations, finite so a hung provider
// can't block a non-interactive run forever.
const DefaultProviderTimeout = 120 * time.Second

// DefaultCacheTTL is how long a cached answer stays reusable unless overridden.
const DefaultCacheTTL = 30 * 24 * time.Hour

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
			Auth:       "bearer",
			Timeout:    DefaultProviderTimeout,
		},
		Rerank:  Rerank{Auth: "bearer", Timeout: DefaultProviderTimeout},
		Storage: Storage{Backend: "sqlite"},
		Cache:   Cache{TTL: DefaultCacheTTL},
		Log:     Log{Level: slog.LevelInfo, Format: "text"},
	}
}

// FlagOverrides carries the command-line flag values that outrank env and file
// in the precedence chain. A zero field means "not set on the command line" and
// is left untouched; Verbose is the exception — when true it forces debug level,
// but an explicit LogLevel still beats it.
type FlagOverrides struct {
	LogLevel  string // "" = unset; e.g. "debug", "info", "warn", "error"
	LogFormat string // "" = unset; "text" or "json"
	Verbose   bool   // true forces debug level unless LogLevel is set
}

// Load builds a Config from defaults, then the TOML file at path (skipped when
// path is empty or the file is absent), then LORE_* variables read via getenv.
// It is Resolve with no flag overrides.
func Load(path string, getenv func(string) string) (Config, error) {
	return Resolve(path, getenv, FlagOverrides{})
}

// Resolve builds a Config with the full precedence chain flags > env > file >
// defaults: defaults, then the TOML file at path, then LORE_* variables, then
// the command-line flag overrides (highest), validating the result.
func Resolve(path string, getenv func(string) string, flags FlagOverrides) (Config, error) {
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
	if err := applyFlags(&cfg, flags); err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyFlags(cfg *Config, flags FlagOverrides) error {
	setString(&cfg.Log.Format, flags.LogFormat)
	switch {
	case flags.LogLevel != "":
		lvl, err := parseLevel(flags.LogLevel)
		if err != nil {
			return err
		}
		cfg.Log.Level = lvl
	case flags.Verbose:
		cfg.Log.Level = slog.LevelDebug
	}
	return nil
}

type fileConfig struct {
	Provider struct {
		BaseURL          string `toml:"base_url"`
		APIKey           string `toml:"api_key"`
		EmbedModel       string `toml:"embed_model"`
		Dimensions       int    `toml:"dimensions"`
		ChatModel        string `toml:"chat_model"`
		Auth             string `toml:"auth"`
		Timeout          string `toml:"timeout"`
		StructuredOutput bool   `toml:"structured_output"`
		ImageInput       bool   `toml:"image_input"`
		DocumentInput    bool   `toml:"document_input"`
	} `toml:"provider"`
	Storage struct {
		Backend string `toml:"backend"`
		Path    string `toml:"path"`
	} `toml:"storage"`
	Rerank struct {
		BaseURL string `toml:"base_url"`
		APIKey  string `toml:"api_key"`
		Auth    string `toml:"auth"`
		Model   string `toml:"model"`
		Timeout string `toml:"timeout"`
	} `toml:"rerank"`
	Ingest struct {
		Concurrency int `toml:"concurrency"`
	} `toml:"ingest"`
	Cache struct {
		Enabled bool   `toml:"enabled"`
		TTL     string `toml:"ttl"`
	} `toml:"cache"`
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
	setString(&cfg.Provider.Auth, fc.Provider.Auth)
	if fc.Provider.Timeout != "" {
		d, err := parseTimeout(fc.Provider.Timeout)
		if err != nil {
			return err
		}
		cfg.Provider.Timeout = d
	}
	setString(&cfg.Storage.Backend, fc.Storage.Backend)
	setString(&cfg.Storage.Path, fc.Storage.Path)
	setString(&cfg.Rerank.BaseURL, fc.Rerank.BaseURL)
	setString(&cfg.Rerank.APIKey, fc.Rerank.APIKey)
	setString(&cfg.Rerank.Auth, fc.Rerank.Auth)
	setString(&cfg.Rerank.Model, fc.Rerank.Model)
	if fc.Rerank.Timeout != "" {
		d, err := parseTimeout(fc.Rerank.Timeout)
		if err != nil {
			return err
		}
		cfg.Rerank.Timeout = d
	}
	if fc.Ingest.Concurrency != 0 {
		cfg.Ingest.Concurrency = fc.Ingest.Concurrency
	}
	if fc.Cache.Enabled {
		cfg.Cache.Enabled = true
	}
	if fc.Cache.TTL != "" {
		d, err := parseCacheTTL(fc.Cache.TTL)
		if err != nil {
			return err
		}
		cfg.Cache.TTL = d
	}
	setString(&cfg.Log.Format, fc.Log.Format)
	if fc.Provider.Dimensions != 0 {
		cfg.Provider.Dimensions = fc.Provider.Dimensions
	}
	if fc.Provider.StructuredOutput {
		cfg.Provider.StructuredOutput = true
	}
	if fc.Provider.ImageInput {
		cfg.Provider.ImageInput = true
	}
	if fc.Provider.DocumentInput {
		cfg.Provider.DocumentInput = true
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
	setString(&cfg.Provider.Auth, getenv("LORE_AUTH"))
	if v := getenv("LORE_TIMEOUT"); v != "" {
		d, err := parseTimeout(v)
		if err != nil {
			return err
		}
		cfg.Provider.Timeout = d
	}
	setString(&cfg.Rerank.BaseURL, getenv("LORE_RERANK_BASE_URL"))
	setString(&cfg.Rerank.APIKey, getenv("LORE_RERANK_API_KEY"))
	setString(&cfg.Rerank.Auth, getenv("LORE_RERANK_AUTH"))
	setString(&cfg.Rerank.Model, getenv("LORE_RERANK_MODEL"))
	if v := getenv("LORE_RERANK_TIMEOUT"); v != "" {
		d, err := parseTimeout(v)
		if err != nil {
			return err
		}
		cfg.Rerank.Timeout = d
	}
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
	if v := getenv("LORE_INGEST_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("config: %w: LORE_INGEST_CONCURRENCY %q is not an integer", domain.ErrInvalidArgument, v)
		}
		cfg.Ingest.Concurrency = n
	}
	if err := applyBoolEnv(&cfg.Provider.StructuredOutput, getenv, "LORE_STRUCTURED_OUTPUT"); err != nil {
		return err
	}
	if err := applyBoolEnv(&cfg.Provider.ImageInput, getenv, "LORE_IMAGE_INPUT"); err != nil {
		return err
	}
	if err := applyBoolEnv(&cfg.Provider.DocumentInput, getenv, "LORE_DOCUMENT_INPUT"); err != nil {
		return err
	}
	if err := applyBoolEnv(&cfg.Cache.Enabled, getenv, "LORE_CACHE"); err != nil {
		return err
	}
	if v := getenv("LORE_CACHE_TTL"); v != "" {
		d, err := parseCacheTTL(v)
		if err != nil {
			return err
		}
		cfg.Cache.TTL = d
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

// applyBoolEnv overlays a boolean environment variable onto dst when set.
func applyBoolEnv(dst *bool, getenv func(string) string, key string) error {
	v := getenv(key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("config: %w: %s %q is not a boolean", domain.ErrInvalidArgument, key, v)
	}
	*dst = b
	return nil
}

// parseTimeout parses a Go duration string (e.g. "30s", "2m", "0") into a
// non-negative timeout. A negative or malformed value is ErrInvalidArgument.
func parseTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("config: %w: timeout %q is not a duration", domain.ErrInvalidArgument, s)
	}
	if d < 0 {
		return 0, fmt.Errorf("config: %w: timeout must not be negative, got %s", domain.ErrInvalidArgument, s)
	}
	return d, nil
}

// parseCacheTTL parses a Go duration string into a non-negative cache TTL.
func parseCacheTTL(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("config: %w: cache ttl %q is not a duration", domain.ErrInvalidArgument, s)
	}
	if d < 0 {
		return 0, fmt.Errorf("config: %w: cache ttl must not be negative, got %s", domain.ErrInvalidArgument, s)
	}
	return d, nil
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
	if cfg.Provider.Auth != "bearer" && cfg.Provider.Auth != "api-key" {
		return fmt.Errorf("config: %w: provider auth %q (want \"bearer\" or \"api-key\")", domain.ErrInvalidArgument, cfg.Provider.Auth)
	}
	if cfg.Rerank.Auth != "bearer" && cfg.Rerank.Auth != "api-key" {
		return fmt.Errorf("config: %w: rerank auth %q (want \"bearer\" or \"api-key\")", domain.ErrInvalidArgument, cfg.Rerank.Auth)
	}
	if cfg.Ingest.Concurrency < 0 {
		return fmt.Errorf("config: %w: ingest concurrency must not be negative, got %d", domain.ErrInvalidArgument, cfg.Ingest.Concurrency)
	}
	if cfg.Storage.Backend != "sqlite" && cfg.Storage.Backend != "memory" {
		return fmt.Errorf("config: %w: storage backend %q (want \"sqlite\" or \"memory\")", domain.ErrInvalidArgument, cfg.Storage.Backend)
	}
	if cfg.Cache.TTL < 0 {
		return fmt.Errorf("config: %w: cache ttl must not be negative, got %s", domain.ErrInvalidArgument, cfg.Cache.TTL)
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
