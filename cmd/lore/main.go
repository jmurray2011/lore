// Command lore is the composition root: the only place that imports adapters.
// It loads config, builds the logger, wires adapters into use cases, and hands
// the resulting dependencies to internal/cli.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jmurray2011/lore/internal/adapters/cache"
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

	// cleanup and logger are populated by build (invoked in PersistentPreRunE)
	// once config is resolved. cleanup is deferred here so the store closes even
	// when a command fails, which cobra's PostRun hooks would skip.
	var (
		cleanup func() error
		logger  *slog.Logger
	)
	defer func() {
		if cleanup != nil {
			_ = cleanup()
		}
	}()

	defaultPath, _ := config.DefaultPath()

	build := func(_ context.Context, opts cli.GlobalOptions) (cli.Deps, error) {
		path := defaultPath
		if opts.ConfigPath != "" {
			path = opts.ConfigPath
		}
		cfg, err := config.Resolve(path, os.Getenv, config.FlagOverrides{
			LogLevel:  opts.LogLevel,
			LogFormat: opts.LogFormat,
			Verbose:   opts.Verbose,
		})
		if err != nil {
			return cli.Deps{}, err
		}
		logger = config.NewLogger(cfg.Log, stderr)

		store, err := openStorage(cfg.Storage)
		if err != nil {
			return cli.Deps{}, err
		}
		cleanup = store.close

		auth := openai.AuthBearer
		if cfg.Provider.Auth == "api-key" {
			auth = openai.AuthAPIKey
		}

		// One client shared by embedder and generator (connection reuse); its
		// per-request timeout (0 disables) is the safety net against a hung
		// provider in non-interactive use (decision 36).
		httpClient := &http.Client{Timeout: cfg.Provider.Timeout}

		embedder, err := openai.NewEmbedder(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.EmbedModel, cfg.Provider.Dimensions, auth, httpClient)
		if err != nil {
			return cli.Deps{}, err
		}
		caps := openai.Capabilities{
			StructuredOutput: cfg.Provider.StructuredOutput,
			ImageInput:       cfg.Provider.ImageInput,
			DocumentInput:    cfg.Provider.DocumentInput,
		}
		gen, err := openai.NewGenerator(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.ChatModel, caps, auth, httpClient)
		if err != nil {
			return cli.Deps{}, err
		}
		// Opt-in answer cache (decision): reuse a synthesized answer for the same
		// question + grounding across runs, unless --no-cache is set. The salt
		// scopes the cache to this model + prompt identity so a model or prompt
		// change can't serve a now-wrong answer.
		var generator app.Generator = gen
		if cfg.Cache.Enabled && !opts.NoCache {
			salt := cfg.Provider.ChatModel + "\x00" + strconv.FormatBool(cfg.Provider.StructuredOutput) + "\x00" + openai.PromptVersion
			generator = cache.NewGenerator(gen, store.cache, salt, cfg.Cache.TTL, time.Now)
		}

		chunker, err := domain.NewChunker(domain.DefaultChunkSize, domain.DefaultChunkOverlap)
		if err != nil {
			return cli.Deps{}, err
		}

		extractor := extract.NewRouter(extract.New(), docx.New(), pdf.New(), xlsx.New())
		source := fs.NewSource()

		catalog := app.NewCatalog(store.collections, store.docs, embedder)
		ingestor := app.NewIngestor(store.collections, store.docs, store.index, embedder, extractor, source, chunker, app.WithConcurrency(cfg.Ingest.Concurrency))
		querier := app.NewQuerier(store.collections, store.index, store.docs, embedder)
		remover := app.NewRemover(store.collections, store.docs, store.index)
		return cli.Deps{
			Catalog: catalog,
			Ingest:  ingestor,
			Sync:    app.NewSyncer(catalog, ingestor, remover, source),
			Query:   querier,
			Ask:     app.NewAsker(querier, generator),
			Remove:  remover,
		}, nil
	}

	root := cli.NewRootCommand(build, fmt.Sprintf("%s (commit %s, built %s)", version, commit, date), stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		if logger != nil {
			logger.Debug("command failed", "err", err)
		}
		return fail(err)
	}
	return 0
}

// storage holds the persistence ports for one backend plus a close hook.
type storage struct {
	collections app.CollectionRepository
	docs        app.DocumentRepository
	index       app.VectorIndex
	cache       app.AnswerCache
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
			cache:       memstore.NewAnswerCache(),
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
			cache:       s.Cache(),
			close:       s.Close,
		}, nil
	default:
		// config validation rejects unknown backends; this stays defensive.
		return storage{}, fmt.Errorf("lore: %w: unknown storage backend %q", domain.ErrInvalidArgument, cfg.Backend)
	}
}
