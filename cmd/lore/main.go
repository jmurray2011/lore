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
	"path/filepath"

	"github.com/jmurray2011/lore/internal/adapters/docx"
	"github.com/jmurray2011/lore/internal/adapters/extract"
	"github.com/jmurray2011/lore/internal/adapters/fs"
	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/adapters/openai"
	"github.com/jmurray2011/lore/internal/adapters/pdf"
	"github.com/jmurray2011/lore/internal/adapters/sqlite"
	"github.com/jmurray2011/lore/internal/adapters/xlsx"
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

	store, err := openStorage(cfg.Storage)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = store.close() }()

	auth := openai.AuthBearer
	if cfg.Provider.Auth == "api-key" {
		auth = openai.AuthAPIKey
	}

	embedder, err := openai.NewEmbedder(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.EmbedModel, cfg.Provider.Dimensions, auth, nil)
	if err != nil {
		return fail(err)
	}
	caps := openai.Capabilities{
		StructuredOutput: cfg.Provider.StructuredOutput,
		ImageInput:       cfg.Provider.ImageInput,
		DocumentInput:    cfg.Provider.DocumentInput,
	}
	generator, err := openai.NewGenerator(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.ChatModel, caps, auth, nil)
	if err != nil {
		return fail(err)
	}

	chunker, err := domain.NewChunker(domain.DefaultChunkSize, domain.DefaultChunkOverlap)
	if err != nil {
		return fail(err)
	}

	extractor := extract.NewRouter(extract.New(), docx.New(), pdf.New(), xlsx.New())

	querier := app.NewQuerier(store.collections, store.index, store.docs, embedder)
	deps := cli.Deps{
		Catalog: app.NewCatalog(store.collections, embedder),
		Ingest:  app.NewIngestor(store.collections, store.docs, store.index, embedder, extractor, fs.NewSource(), chunker, app.WithConcurrency(cfg.Ingest.Concurrency)),
		Query:   querier,
		Ask:     app.NewAsker(querier, generator),
		Remove:  app.NewRemover(store.collections, store.docs, store.index),
	}

	root := cli.NewRootCommand(deps, fmt.Sprintf("%s (commit %s, built %s)", version, commit, date), stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		logger.Debug("command failed", "err", err)
		return fail(err)
	}
	return 0
}

// storage holds the three persistence ports for one backend plus a close hook.
type storage struct {
	collections app.CollectionRepository
	docs        app.DocumentRepository
	index       app.VectorIndex
	close       func() error
}

// openStorage selects the persistence backend from config. The "sqlite" backend
// (default) opens one DB file, resolving an empty path to config.DefaultDBPath
// and creating its parent directory; "memory" uses the in-memory reference
// adapter (no persistence across processes).
func openStorage(cfg config.Storage) (storage, error) {
	switch cfg.Backend {
	case "memory":
		return storage{
			collections: memstore.NewCollectionRepository(),
			docs:        memstore.NewDocumentRepository(),
			index:       memstore.NewVectorIndex(),
			close:       func() error { return nil },
		}, nil
	case "sqlite":
		path := cfg.Path
		if path == "" {
			p, err := config.DefaultDBPath()
			if err != nil {
				return storage{}, err
			}
			path = p
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return storage{}, fmt.Errorf("lore: create data directory: %w", err)
		}
		s, err := sqlite.Open(path)
		if err != nil {
			return storage{}, err
		}
		return storage{
			collections: s.Collections(),
			docs:        s.Documents(),
			index:       s.Vectors(),
			close:       s.Close,
		}, nil
	default:
		// config validation rejects unknown backends; this stays defensive.
		return storage{}, fmt.Errorf("lore: %w: unknown storage backend %q", domain.ErrInvalidArgument, cfg.Backend)
	}
}
