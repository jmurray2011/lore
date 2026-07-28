package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/domain"
)

// starterConfig is the commented config.toml `lore config init` writes: a
// ready-to-edit minimal setup with the common knobs, everything optional. Only
// the keys lore actually recognizes appear uncommented, so a freshly written
// file loads clean (no "unrecognized key" warning).
const starterConfig = `# lore configuration.
# Precedence: command-line flags > LORE_* environment variables > this file >
# built-in defaults. Every setting is optional. See docs/configuration.md for the
# full reference.

[provider]
# OpenAI-compatible endpoint for embeddings and chat (OpenAI, Azure, Ollama,
# vLLM, LM Studio, OpenRouter, or any local server).
base_url = "https://api.openai.com/v1"
# API key. Prefer the LORE_API_KEY environment variable for secrets.
# api_key = "sk-..."
embed_model = "text-embedding-3-small"
dimensions  = 1536
chat_model  = "gpt-4o-mini"
# auth = "bearer"   # "bearer" (default) or "api-key" (Azure OpenAI)

# Fully local, no API key, with Ollama (replace the block above):
#   base_url    = "http://localhost:11434/v1"
#   embed_model = "nomic-embed-text"
#   dimensions  = 768
#   chat_model  = "llama3.1"

# [storage]
# backend = "sqlite"   # "sqlite" (default) or "memory"
# path    = ""         # sqlite DB file; empty = alongside this config

# [retrieval]
# hybrid = false       # BM25 + vector hybrid retrieval by default (per-command --hybrid overrides)

# [rerank]             # optional cross-encoder reranker (Cohere-style; a separate endpoint)
# base_url = ""
# api_key  = ""
# model    = ""

# [cache]
# enabled = false      # reuse synthesized answers across runs
`

// newConfigCmd is the `lore config` group: inspect and scaffold the config file.
// Its subcommands only touch the config file, so it overrides the root's
// PersistentPreRunE with a no-op — skipping the runtime build (storage/provider
// wiring and its side effects) and letting `config init` target a path that does
// not exist yet without tripping the missing-config guard.
func newConfigCmd(defaultConfigPath string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "config",
		Short:             "Show or scaffold lore's configuration file",
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}
	cmd.AddCommand(newConfigPathCmd(defaultConfigPath), newConfigInitCmd(defaultConfigPath))
	return cmd
}

// resolveConfigTarget is the config path a config subcommand acts on: an explicit
// positional argument, else the global --config flag, else the default location.
func resolveConfigTarget(cmd *cobra.Command, args []string, defaultConfigPath string) string {
	if len(args) == 1 && args[0] != "" {
		return args[0]
	}
	if p := flagString(cmd, "config"); p != "" {
		return p
	}
	return defaultConfigPath
}

func newConfigPathCmd(defaultConfigPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path lore will use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := defaultConfigPath
			if f := flagString(cmd, "config"); f != "" {
				p = f
			}
			return render(cmd, map[string]string{"path": p}, p)
		},
	}
}

func newConfigInitCmd(defaultConfigPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Write a commented starter config file",
		Long: "Write a commented starter config.toml to [path], the --config path, or the default\n" +
			"location, creating parent directories. Refuses to overwrite an existing file.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := resolveConfigTarget(cmd, args, defaultConfigPath)
			if target == "" {
				return fmt.Errorf("%w: could not determine a config path; pass one explicitly", domain.ErrInvalidArgument)
			}
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("config already exists at %s; edit it, or remove it first", target)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create config directory: %w", err)
			}
			if err := os.WriteFile(target, []byte(starterConfig), 0o600); err != nil {
				return fmt.Errorf("write config: %w", err)
			}
			return render(cmd, map[string]any{"path": target, "created": true},
				fmt.Sprintf("Wrote a starter config to **%s**.\nEdit it (set your provider and API key), then run `lore init`.", target))
		},
	}
}
