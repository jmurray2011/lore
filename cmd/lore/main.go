// Command lore is the composition root: the only place that imports adapters.
// It loads config, builds the logger, wires adapters into use cases, and hands
// the resulting dependencies to internal/cli.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/jmurray2011/lore/internal/adapters/extract"
	"github.com/jmurray2011/lore/internal/adapters/fs"
	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/adapters/openai"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/cli"
	"github.com/jmurray2011/lore/internal/config"
	"github.com/jmurray2011/lore/internal/domain"
)

// Set by goreleaser via -ldflags (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "lore: %v\n", err)
		return cli.ExitCode(err)
	}

	path, _ := config.DefaultPath()
	cfg, err := config.Load(path, os.Getenv)
	if err != nil {
		return fail(err)
	}
	logger := config.NewLogger(cfg.Log, stderr)

	// memstore is in-memory and per-process; persistence arrives with sqlite.
	collections := memstore.NewCollectionRepository()
	docs := memstore.NewDocumentRepository()
	index := memstore.NewVectorIndex()

	embedder, err := openai.NewEmbedder(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.EmbedModel, cfg.Provider.Dimensions, nil)
	if err != nil {
		return fail(err)
	}
	generator, err := openai.NewGenerator(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.ChatModel, nil)
	if err != nil {
		return fail(err)
	}

	chunker, err := domain.NewChunker(domain.DefaultChunkSize, domain.DefaultChunkOverlap)
	if err != nil {
		return fail(err)
	}

	querier := app.NewQuerier(collections, index, docs, embedder)
	deps := cli.Deps{
		Catalog: app.NewCatalog(collections, embedder),
		Ingest:  app.NewIngestor(collections, docs, index, embedder, extract.New(), fs.NewSource(), chunker),
		Query:   querier,
		Ask:     app.NewAsker(querier, generator),
	}

	root := cli.NewRootCommand(deps, fmt.Sprintf("%s (commit %s, built %s)", version, commit, date), stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		logger.Debug("command failed", "err", err)
		return fail(err)
	}
	return 0
}
