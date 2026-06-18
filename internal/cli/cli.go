// Package cli is lore's driving adapter: it translates cobra commands into use
// case calls and renders results. stdout carries data (human text, or JSON with
// --json); logs and errors go to stderr. It holds no business logic
// and imports no storage/provider adapters — the composition root wires those
// into Deps.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Deps are the use cases the commands invoke, wired by the composition root.
type Deps struct {
	Catalog *app.Catalog
	Ingest  *app.Ingestor
	Sync    *app.Syncer
	Query   *app.Querier
	Ask     *app.Asker
	// Retriever composes retrieval (hybrid/rerank/mmr/recency/cap) for query/ask;
	// the one source of truth shared with the eval harness and the MCP server.
	Retriever *app.Retriever
	// Rerank is nil when no rerank provider is configured; commands that need it
	// (rerank, query/ask --rerank) report a usage error in that case.
	Rerank *app.Reranker
	Remove *app.Remover
	// Replay re-runs an ask manifest to verify the answer reproduces (lore replay).
	Replay *app.Replayer
	// Tokens counts tokens for --budget token-bounded retrieval (query/ask).
	Tokens app.TokenCounter
	// Export and Import move a collection to/from a single portable artifact file.
	Export *app.Exporter
	Import *app.Importer
	// Verify checks an answer's faithfulness (ask --verify); Eval runs an eval set
	// (lore eval). Both may be nil if the runtime was built without them.
	Verify *app.Checker
	Eval   *app.Evaluator
	// Index is the vector index, exposed read-only for the mcp server's
	// collection_status chunk count. Other commands reach vectors via use cases.
	Index app.VectorIndex
	// Log is lore's configured logger (stderr), handed to the long-running mcp
	// server so it honors --log-level/--log-format; nil is tolerated.
	Log *slog.Logger
	// RetrievalHybrid is the configured default for hybrid retrieval; it sets the
	// default value of query/ask --hybrid (overridable per command).
	RetrievalHybrid bool
	// ChatModel is the configured chat model name, recorded in an ask manifest's
	// generation identity. Empty is tolerated (manifest records what it knows).
	ChatModel string
}

// GlobalOptions are the resolved global flags the composition root needs to
// build the runtime: which config file to read and how to log. The Builder
// turns these into Deps.
type GlobalOptions struct {
	ConfigPath string // --config; empty means the default location
	LogLevel   string // --log-level; empty means unset
	LogFormat  string // --log-format; empty means unset
	Verbose    bool   // -v/--verbose
	NoCache    bool   // --no-cache; bypass the answer cache for this run
}

// Builder constructs the runtime dependencies from the resolved global options.
// The composition root provides it; the root command invokes it once, in
// PersistentPreRunE — after global flags are parsed — so --config can redirect
// configuration loading before anything is wired.
type Builder func(context.Context, GlobalOptions) (Deps, error)

// NewRootCommand builds the lore command tree, writing data to out and errors to
// errOut. The build function is invoked once before any subcommand runs to
// produce the dependencies the commands share. Errors returned from Execute map
// to process exit codes via ExitCode.
func NewRootCommand(build Builder, version string, out, errOut io.Writer) *cobra.Command {
	// deps is populated by PersistentPreRunE before any subcommand's RunE; the
	// subcommands capture &deps so they see the built value at run time.
	var deps Deps

	root := &cobra.Command{
		Use:           "lore",
		Short:         "Fast, scriptable RAG and LLM operations over specific document sets",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)
	pf := root.PersistentFlags()
	pf.Bool("json", false, "emit machine-readable JSON to stdout")
	pf.Bool("no-color", false, "disable ANSI color in human output")
	pf.String("config", "", "path to the TOML config file (default: <user-config-dir>/lore/config.toml)")
	pf.String("log-level", "", "log level: debug, info, warn, or error")
	pf.String("log-format", "", "log format: text or json")
	pf.BoolP("verbose", "v", false, "verbose logging (shorthand for --log-level debug)")
	pf.Bool("no-cache", false, "bypass the answer cache for this run (ask/synthesize)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %v", domain.ErrInvalidArgument, err)
	})
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		d, err := build(cmd.Context(), GlobalOptions{
			ConfigPath: flagString(cmd, "config"),
			LogLevel:   flagString(cmd, "log-level"),
			LogFormat:  flagString(cmd, "log-format"),
			Verbose:    flagBool(cmd, "verbose"),
			NoCache:    flagBool(cmd, "no-cache"),
		})
		if err != nil {
			return err
		}
		deps = d
		return nil
	}
	root.AddCommand(
		newInitCmd(&deps),
		newAddCmd(&deps),
		newSyncCmd(&deps),
		newLsCmd(&deps),
		newStatusCmd(&deps),
		newDocsCmd(&deps),
		newCatCmd(&deps),
		newDiffCmd(&deps),
		newQueryCmd(&deps),
		newAskCmd(&deps),
		newReplayCmd(&deps),
		newEvalCmd(&deps),
		newSynthesizeCmd(&deps),
		newRerankCmd(&deps),
		newExportCmd(&deps),
		newImportCmd(&deps),
		newRmCmd(&deps),
		newMCPCmd(&deps),
	)
	return root
}

func flagString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func flagBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// ExitCode maps a command error to a lore exit code: 0 ok,
// 1 runtime, 2 usage, 3 not found, 4 invariant violation.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, app.ErrReproducibleUnsupported):
		return 2
	case errors.Is(err, app.ErrNotFound):
		return 3
	case errors.Is(err, domain.ErrSpaceMismatch), errors.Is(err, domain.ErrChunkerMismatch):
		return 4
	case errors.Is(err, app.ErrGateUnmet):
		return 5
	default:
		return 1
	}
}

// render writes v as indented JSON when --json is set; otherwise it treats the
// human string as Markdown — glamour-rendered for an interactive terminal, or
// emitted raw (clean for pipes, tests, and --no-color).
func render(cmd *cobra.Command, jsonValue any, markdown string) error {
	w := cmd.OutOrStdout()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(jsonValue)
	}
	if markdown == "" {
		return nil
	}
	if richEnabled(cmd) {
		if out, err := renderMarkdown(w, markdown); err == nil {
			_, err := io.WriteString(w, out)
			return err
		}
		// Fall through to raw Markdown if glamour fails for any reason.
	}
	_, err := fmt.Fprintln(w, markdown)
	return err
}

// Stable JSON output shapes.

type collectionView struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  string `json:"created_at"`
}

func viewCollection(c *domain.Collection) collectionView {
	return collectionView{
		Name:       c.Name,
		Model:      c.Space.Model,
		Dimensions: c.Space.Dimensions,
		CreatedAt:  c.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// statusView is the collection view plus the document count and the pinned
// chunker, shown only by `status` (a single collection). ls deliberately omits
// the count — it would cost one query per listed collection.
type statusView struct {
	collectionView
	Documents int    `json:"documents"`
	Chunker   string `json:"chunker"`
	// LastIngest is the most recent document ingestion time (max IngestedAt):
	// the content-derived "as of when" that advances on any re-ingest, unlike
	// CreatedAt. Omitted for an empty collection.
	LastIngest string `json:"last_ingest_at,omitempty"`
	// Digest is the corpus content identity (hex sha256 over the document set);
	// it flips on any add, removal, or edit. The field a provenance snapshot
	// should stamp instead of created_at.
	Digest string `json:"corpus_digest"`
}

// chunkerLabel renders a collection's pinned chunker for display, naming the
// read-only state of a collection that predates chunker pinning.
func chunkerLabel(c *domain.Collection) string {
	if c.Chunker.IsZero() {
		return "unpinned (legacy; rebuild to ingest)"
	}
	return c.Chunker.String()
}

type hitView struct {
	ChunkID string  `json:"chunk_id"`
	Source  string  `json:"source"`
	Seq     int     `json:"seq"`
	Score   float64 `json:"score"`
	// RerankScore is present only on hits that went through a reranker; omitempty
	// keeps the schema additive for query/synthesize consumers that never rerank.
	RerankScore *float64 `json:"rerank_score,omitempty"`
	// Collection names the hit's origin collection, set only for cross-collection
	// (multi-collection) queries; omitempty keeps single-collection output
	// byte-for-byte unchanged.
	Collection string `json:"collection,omitempty"`
	// Metadata is the chunk's document-level attributes; omitempty keeps output
	// unchanged for documents ingested without metadata.
	Metadata domain.Metadata `json:"metadata,omitempty"`
	Text     string          `json:"text"`
}

// fromRef identifies the source chunk a query --from-collection group was driven
// by: its chunk ID, source URI, and position. It carries enough provenance to
// trace each group back to the chunk whose vector produced it.
type fromRef struct {
	ChunkID string `json:"chunk_id"`
	Source  string `json:"source"`
	Seq     int    `json:"seq"`
}

// fromGroupView is one query --from-collection result: the source chunk and the
// target hits its vector retrieved. The hits reuse the standard hit schema.
type fromGroupView struct {
	From fromRef   `json:"from"`
	Hits []hitView `json:"hits"`
}

type chunkView struct {
	ChunkID string `json:"chunk_id"`
	Seq     int    `json:"seq"`
	// HeadingPath is the document section the chunk came from (structure-aware
	// chunkers); omitempty keeps output unchanged for chunks without one.
	HeadingPath string `json:"heading_path,omitempty"`
	Text        string `json:"text"`
}

type citationView struct {
	ChunkID string `json:"chunk_id"`
	Source  string `json:"source"`
	Seq     int    `json:"seq"`
	// Collection names the cited chunk's origin collection, set only for
	// cross-collection answers; omitempty keeps single-collection output
	// byte-for-byte unchanged.
	Collection string `json:"collection,omitempty"`
}

type answerView struct {
	Text      string         `json:"text"`
	Citations []citationView `json:"citations"`
	Grounded  bool           `json:"grounded"`
	// Expansions carries the full text of cited chunks when --expand is set.
	// omitempty keeps existing --json output byte-for-byte unchanged otherwise.
	Expansions []chunkView `json:"expansions,omitempty"`
	// Explain carries the retrieval diagnostics when --explain is set. omitempty
	// keeps ask --json byte-for-byte unchanged without the flag. (query --json is
	// a bare hit array — the synthesize contract — so its explain goes to stderr
	// instead; see writeQueryExplain.)
	Explain *explainView `json:"explain,omitempty"`
	// GroundingTokens is the token count of the chunks that grounded the answer,
	// reported only when --budget is set; omitempty keeps output unchanged
	// otherwise.
	GroundingTokens *int `json:"grounding_tokens,omitempty"`
	// Verification carries the per-claim faithfulness verdicts when --verify is set;
	// SupportRate is the fraction of claims supported. omitempty keeps output
	// unchanged without --verify.
	Verification []verificationClaimView `json:"verification,omitempty"`
	SupportRate  *float64                `json:"support_rate,omitempty"`
	// Manifest is the reproducible provenance record of the ask: corpus digests,
	// retrieval config, generation identity, cited chunks, and an answer digest.
	// Present on --json so `lore replay` can re-run the exhibit. omitempty keeps
	// human and streamed output unaffected.
	Manifest *app.Manifest `json:"manifest,omitempty"`
}

// verificationClaimView is one claim's faithfulness verdict in --json output.
type verificationClaimView struct {
	Claim       string   `json:"claim"`
	CitedChunks []string `json:"cited_chunks"`
	Verdict     string   `json:"verdict"`
	Rationale   string   `json:"rationale,omitempty"`
}

// explainView is the --explain diagnostic: the returned hits' score
// distribution plus the best candidate just outside the top-k (next_score), so
// a low-everywhere distribution (retrieval starvation) is distinguishable from a
// high-but-uncited chunk (a synthesis miss). It carries scores, not chunk text;
// --expand is the flag for text.
type explainView struct {
	Returned  []explainHit `json:"returned"`
	NextScore *float64     `json:"next_score"` // null when there is no further candidate
	Stats     explainStats `json:"stats"`
}

// explainHit is one returned chunk in the distribution. RerankScore is set when
// two-stage retrieval (--rerank) reordered the hits; Cited is set only for ask
// (which has an answer to cite from). Both are omitted otherwise.
type explainHit struct {
	Score       float64  `json:"score"`
	RerankScore *float64 `json:"rerank_score,omitempty"`
	Source      string   `json:"source"`
	Seq         int      `json:"seq"`
	Cited       *bool    `json:"cited,omitempty"`
}

type explainStats struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

type docView struct {
	Source     string          `json:"source"`
	Hash       string          `json:"hash"`
	IngestedAt string          `json:"ingested_at"`
	Metadata   domain.Metadata `json:"metadata,omitempty"`
}

// transferView is the export/import summary. Encrypted reports whether the
// artifact was/is age-encrypted; Output (export only) is the destination path.
type transferView struct {
	Collection string `json:"collection"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	Documents  int    `json:"documents"`
	Chunks     int    `json:"chunks"`
	Encrypted  bool   `json:"encrypted"`
	Output     string `json:"output,omitempty"`
}

type rmView struct {
	Removed    string `json:"removed"` // "collection", "document", or "chunks"
	Collection string `json:"collection"`
	Document   string `json:"document,omitempty"`
	// ChunkIDs lists the chunks actually removed by rm --chunk; omitted for the
	// collection/document cases so their output is unchanged.
	ChunkIDs []string `json:"chunk_ids,omitempty"`
}
