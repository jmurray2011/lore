// Command lore is the composition root: the only place that imports adapters.
// It loads config, builds the logger, wires adapters into use cases, and hands
// the resulting dependencies to internal/cli.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
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
	"github.com/jmurray2011/lore/internal/adapters/httpjson"
	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/adapters/openai"
	"github.com/jmurray2011/lore/internal/adapters/pdf"
	"github.com/jmurray2011/lore/internal/adapters/rerank"
	"github.com/jmurray2011/lore/internal/adapters/sqlite"
	"github.com/jmurray2011/lore/internal/adapters/tiktoken"
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
	defaultPath, _ := config.DefaultPath()

	fail := func(err error) int {
		if msg, ok := authGuidance(err, defaultPath); ok {
			_, _ = fmt.Fprintf(stderr, "lore: %s\n", msg)
		} else {
			_, _ = fmt.Fprintf(stderr, "lore: %v\n", err)
		}
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

	build := func(_ context.Context, opts cli.GlobalOptions) (cli.Deps, error) {
		path := defaultPath
		if opts.ConfigPath != "" {
			path = opts.ConfigPath
			// An explicitly requested config file that is not there is a mistake,
			// not a cue to fall back to defaults: fail loudly so the user does not
			// run against the wrong endpoint/DB believing their config was loaded.
			// (The default path staying absent is still fine — that is defaults+env.)
			if _, statErr := os.Stat(path); statErr != nil {
				if errors.Is(statErr, iofs.ErrNotExist) {
					return cli.Deps{}, fmt.Errorf("%w: --config %s: file does not exist", domain.ErrInvalidArgument, path)
				}
				return cli.Deps{}, fmt.Errorf("config: %s: %w", path, statErr)
			}
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
		// Surface silently-ignored config keys (typos, misplaced keys) now that the
		// logger exists; without this a misspelled setting reads exactly like an
		// unset one and the user never learns why their config had no effect.
		if unknown := config.UndecodedKeys(path); len(unknown) > 0 {
			logger.Warn("ignoring unrecognized config keys", "path", path, "keys", unknown)
		}

		store, err := openStorage(cfg.Storage)
		if err != nil {
			return cli.Deps{}, err
		}
		cleanup = store.close

		// Embed and chat are independently-targetable: each resolves its own
		// connection (per-role override → shared provider.* → default), so one
		// process can embed against one endpoint and chat against another. A
		// separate http.Client per role carries that role's timeout (0 disables) —
		// the safety net against a hung provider in non-interactive use.
		embedConn := cfg.Provider.EmbedConnection()
		chatConn := cfg.Provider.ChatConnection()

		embedder, err := openai.NewEmbedder(embedConn.BaseURL, embedConn.APIKey, cfg.Provider.EmbedModel, cfg.Provider.Dimensions, authStyle(embedConn.Auth), &http.Client{Timeout: embedConn.Timeout})
		if err != nil {
			return cli.Deps{}, err
		}
		caps := openai.Capabilities{
			StructuredOutput: cfg.Provider.StructuredOutput,
			ImageInput:       cfg.Provider.ImageInput,
			DocumentInput:    cfg.Provider.DocumentInput,
		}
		gen, err := openai.NewGenerator(chatConn.BaseURL, chatConn.APIKey, cfg.Provider.ChatModel, caps, authStyle(chatConn.Auth), &http.Client{Timeout: chatConn.Timeout})
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

		// Optional rerank provider — a separate Cohere-style endpoint (its own
		// base URL/key/model, decision). Wired only when configured; the rerank
		// command and query/ask --rerank report a usage error when it is nil.
		var reranker *app.Reranker
		if cfg.Rerank.BaseURL != "" && cfg.Rerank.Model != "" {
			rerankAuth := httpjson.AuthBearer
			if cfg.Rerank.Auth == "api-key" {
				rerankAuth = httpjson.AuthAPIKey
			}
			provider, err := rerank.NewReranker(cfg.Rerank.BaseURL, cfg.Rerank.APIKey, cfg.Rerank.Model, rerankAuth, &http.Client{Timeout: cfg.Rerank.Timeout})
			if err != nil {
				return cli.Deps{}, err
			}
			reranker = app.NewReranker(provider)
		}

		counter, err := tiktoken.New()
		if err != nil {
			return cli.Deps{}, err
		}
		chunkers, err := buildChunkers(cfg.Chunk, counter)
		if err != nil {
			return cli.Deps{}, err
		}

		extractor := extract.NewRouter(extract.New(), docx.New(), pdf.New(), xlsx.New())
		source := fs.NewSource()

		catalog := app.NewCatalog(store.collections, store.docs, embedder, chunkers)
		ingestor := app.NewIngestor(store.collections, store.docs, store.index, embedder, extractor, source, chunkers, store.lexical, app.WithConcurrency(cfg.Ingest.Concurrency))
		querier := app.NewQuerier(store.collections, store.index, store.docs, embedder, store.lexical)
		retriever := app.NewRetriever(querier, reranker, store.index)
		remover := app.NewRemover(store.collections, store.docs, store.index, store.lexical)
		asker := app.NewAsker(querier, generator)

		// Faithfulness verification reuses the chat model (decision): a Verifier over
		// the same chat connection, no extra dependency. The Checker fetches cited
		// chunk text via the Catalog as evidence; the Evaluator runs eval sets.
		verifier, err := openai.NewVerifier(chatConn.BaseURL, chatConn.APIKey, cfg.Provider.ChatModel, cfg.Provider.StructuredOutput, authStyle(chatConn.Auth), &http.Client{Timeout: chatConn.Timeout})
		if err != nil {
			return cli.Deps{}, err
		}
		checker := app.NewChecker(verifier, catalog)
		return cli.Deps{
			Catalog:         catalog,
			Ingest:          ingestor,
			Sync:            app.NewSyncer(catalog, ingestor, remover, source),
			Query:           querier,
			Ask:             asker,
			Retriever:       retriever,
			Rerank:          reranker,
			Remove:          remover,
			Replay:          app.NewReplayer(catalog, retriever, asker, store.docs),
			Tokens:          counter,
			Export:          app.NewExporter(store.collections, store.docs, store.index),
			Import:          app.NewImporter(store.collections, store.docs, store.index, remover, store.lexical, embedder),
			Verify:          checker,
			Eval:            app.NewEvaluator(asker, checker),
			Index:           store.index,
			Log:             logger,
			RetrievalHybrid: cfg.Retrieval.Hybrid,
			ChatModel:       cfg.Provider.ChatModel,
			EmbedSpace:      domain.EmbeddingSpace{Model: cfg.Provider.EmbedModel, Dimensions: cfg.Provider.Dimensions},
		}, nil
	}

	root := cli.NewRootCommand(build, fmt.Sprintf("%s (commit %s, built %s)", version, commit, date), defaultPath, stdout, stderr)
	// Show the real resolved config path in --config help instead of the abstract
	// "<user-config-dir>/..." placeholder, so a user knows exactly which file to
	// create or edit.
	if defaultPath != "" {
		if f := root.PersistentFlags().Lookup("config"); f != nil {
			f.Usage = fmt.Sprintf("path to the TOML config file (default: %s)", defaultPath)
		}
	}
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		if logger != nil {
			logger.Debug("command failed", "err", err)
		}
		return fail(err)
	}
	return 0
}

// authGuidance turns a provider authentication rejection (HTTP 401/403) into an
// actionable message that names where to set the key, replacing the raw upstream
// error body a user would otherwise see. It returns ok=false for any other error
// so the caller prints the default form. configPath is the resolved default
// config location, named so a layman knows which file to edit.
func authGuidance(err error, configPath string) (string, bool) {
	var se *httpjson.StatusError
	if !errors.As(err, &se) || (se.Code != http.StatusUnauthorized && se.Code != http.StatusForbidden) {
		return "", false
	}
	return fmt.Sprintf("provider authentication failed (HTTP %d from %s).\n"+
		"  Set an API key:  export LORE_API_KEY=<key>   (or add api_key under [provider] in %s)\n"+
		"  Provider setup and auth styles (OpenAI, Azure, Ollama, local): see docs/configuration.md",
		se.Code, se.Path, configPath), true
}

// authStyle maps a resolved connection's auth string to the openai/httpjson
// auth scheme. Config validation guarantees the value is "bearer" or "api-key",
// so an unrecognized value defensively falls back to bearer.
func authStyle(auth string) openai.AuthStyle {
	if auth == "api-key" {
		return openai.AuthAPIKey
	}
	return openai.AuthBearer
}

// storage holds the persistence ports for one backend plus a close hook.
type storage struct {
	collections app.CollectionRepository
	docs        app.DocumentRepository
	index       app.VectorIndex
	lexical     app.LexicalIndex
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
			lexical:     memstore.NewLexicalIndex(),
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
			lexical:     s.Lexical(),
			cache:       s.Cache(),
			close:       s.Close,
		}, nil
	default:
		// config validation rejects unknown backends; this stays defensive.
		return storage{}, fmt.Errorf("lore: %w: unknown storage backend %q", domain.ErrInvalidArgument, cfg.Backend)
	}
}

// buildChunkers assembles the chunker Registry (and the ChunkerSpec it pins onto
// new collections) from the chunk configuration. The structure strategy routes
// markdown to the heading-aware, token-sized chunker and falls back to the fixed
// word chunker for formats without a structure-aware chunker yet; the fixed
// strategy uses the word chunker for everything.
func buildChunkers(cc config.Chunk, counter *tiktoken.Counter) (domain.Registry, error) {
	switch cc.Strategy {
	case "fixed":
		fixed, err := domain.NewFixedChunker(cc.Size, cc.Overlap)
		if err != nil {
			return domain.Registry{}, err
		}
		spec := domain.ChunkerSpec{Strategy: "fixed", Version: domain.FixedChunkerVersion, Size: cc.Size, Overlap: cc.Overlap, Tokenizer: "words"}
		return domain.NewRegistry(spec, fixed, nil)
	case "structure":
		markdown, err := domain.NewMarkdownChunker(cc.Size, cc.Overlap, cc.ContextPrefix, counter.Count)
		if err != nil {
			return domain.Registry{}, err
		}
		// Plain text (and the default for docx paragraphs + best-effort pdf/xlsx
		// text) uses the paragraph-aware, token-sized text chunker.
		text, err := domain.NewTextChunker(cc.Size, cc.Overlap, counter.Count)
		if err != nil {
			return domain.Registry{}, err
		}
		spec := domain.ChunkerSpec{Strategy: "structure", Version: domain.StructureChunkerVersion, Size: cc.Size, Overlap: cc.Overlap, Tokenizer: tiktoken.EncodingName, ContextPrefix: cc.ContextPrefix}
		return domain.NewRegistry(spec, text, map[string]domain.Chunker{"text/markdown": markdown})
	default:
		// config validation rejects unknown strategies; this stays defensive.
		return domain.Registry{}, fmt.Errorf("lore: %w: unknown chunk strategy %q", domain.ErrInvalidArgument, cc.Strategy)
	}
}
