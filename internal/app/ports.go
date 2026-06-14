// Package app contains the use cases and defines the ports (interfaces)
// they consume. Adapters implement these ports; the conformance suites in
// internal/conformance are their executable contracts.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

// Sentinel errors shared across ports. Adapters wrap these with %w so
// callers can use errors.Is; the CLI maps them to exit codes.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrNoGrounding is returned by Ask in strict mode when retrieval yields no
	// chunks and no attachments were supplied — there is nothing to ground an
	// answer in, so the LLM is not called.
	ErrNoGrounding = errors.New("no grounding")
)

// CollectionRepository persists Collection aggregates.
type CollectionRepository interface {
	// Create fails with ErrAlreadyExists if the name is taken.
	Create(ctx context.Context, c *domain.Collection) error
	// Get fails with ErrNotFound if no such collection exists.
	Get(ctx context.Context, name string) (*domain.Collection, error)
	List(ctx context.Context) ([]*domain.Collection, error)
	// Delete removes the collection record, failing with ErrNotFound if no
	// such collection exists. Cascading removal of its documents and vectors
	// is orchestrated by the use case, which holds the
	// DocumentRepository and VectorIndex; this port cannot reach them.
	Delete(ctx context.Context, name string) error
	// RecordSource adds source to the collection's remembered Sources,
	// idempotently (recording an existing source is a no-op). It fails with
	// ErrNotFound if no such collection exists. Get reflects recorded sources.
	RecordSource(ctx context.Context, name, source string) error
}

// DocumentRepository persists Documents and their Chunks.
type DocumentRepository interface {
	// Upsert stores the document and replaces its chunks atomically.
	Upsert(ctx context.Context, doc *domain.Document, chunks []domain.Chunk) error
	// GetBySource fails with ErrNotFound if the source was never ingested.
	GetBySource(ctx context.Context, collection, sourceURI string) (*domain.Document, error)
	// GetChunks hydrates chunks by ID, preserving input order. IDs with no
	// stored chunk are skipped, so the result may be shorter than the input.
	GetChunks(ctx context.Context, ids []domain.ChunkID) ([]domain.Chunk, error)
	// GetChunksByDocument returns all chunks of one document in seq order. An
	// unknown document (or collection) yields no chunks and no error; existence
	// is the caller's concern (resolve the document via GetBySource first).
	GetChunksByDocument(ctx context.Context, collection string, id domain.DocumentID) ([]domain.Chunk, error)
	// GetChunksByIDs returns the chunks with the given IDs that belong to the
	// collection, in input order. IDs with no stored chunk in the collection are
	// omitted (the caller diffs requested vs returned). An unknown collection
	// yields no chunks and no error; collection existence is the caller's concern.
	GetChunksByIDs(ctx context.Context, collection string, ids []string) ([]domain.Chunk, error)
	// GetDocuments hydrates documents by ID, preserving input order. IDs with no
	// stored document are skipped, so the result may be shorter than the input.
	// Used to attach source provenance to retrieval results.
	GetDocuments(ctx context.Context, ids []domain.DocumentID) ([]*domain.Document, error)
	// ListDocuments returns every document in the collection. An unknown or empty
	// collection yields no documents and no error; collection existence is the
	// CollectionRepository's concern. Order is unspecified.
	ListDocuments(ctx context.Context, collection string) ([]*domain.Document, error)
	// Delete removes the document and its chunks, returning the removed chunk
	// IDs so the use case can delete their vectors via the VectorIndex
	// (this port cannot reach it). Fails with ErrNotFound if no
	// such document exists in the collection.
	Delete(ctx context.Context, collection string, id domain.DocumentID) ([]domain.ChunkID, error)
	// DeleteChunks removes the chunks with the given IDs that belong to the
	// collection, returning the IDs actually removed so the use case can delete
	// their vectors. The owning documents are left in place — a
	// document keeps its record even after losing chunks (sub-document
	// redaction, not document deletion). IDs absent from the collection
	// (unknown, or owned by another collection) are skipped, so the caller can
	// diff requested vs removed; an unknown collection removes nothing. No error
	// for skips — a malformed-ID guard is the caller's concern.
	DeleteChunks(ctx context.Context, collection string, ids []domain.ChunkID) ([]domain.ChunkID, error)
	// DeleteCollection removes every document and its chunks in the collection,
	// returning all removed chunk IDs for the same cascade. A collection with no
	// documents is a no-op (no IDs, no error); collection existence is the
	// CollectionRepository's concern.
	DeleteCollection(ctx context.Context, collection string) ([]domain.ChunkID, error)
}

// VectorEntry pairs a chunk identity with its vector for indexing. Metadata is
// the chunk's document-level attributes, carried on the entry so the index can
// apply a --where filter during Search without reaching into the
// DocumentRepository (the two are separate ports, possibly separate engines).
type VectorEntry struct {
	ChunkID  domain.ChunkID
	Vector   []float32
	Metadata domain.Metadata
}

// VectorIndex stores and searches vectors. It is deliberately dumb: space
// coherence is enforced by the use cases via
// Collection.AcceptsSpace before anything reaches the index.
//
// Semantics:
//   - Upsert replaces entries with the same ChunkID.
//   - Search returns up to k matches, best first (higher score = more
//     similar), considering only entries whose Metadata satisfies filter; the
//     filter is applied before the top-k cut, so the result is exact (no
//     over-fetch). The zero Predicate matches every entry, i.e. no filtering.
//     Unknown collection or k <= 0 yields no matches, no error.
//   - Delete of absent IDs is a no-op.
type VectorIndex interface {
	Upsert(ctx context.Context, collection string, entries []VectorEntry) error
	Search(ctx context.Context, collection string, query []float32, k int, filter domain.Predicate) ([]domain.VectorMatch, error)
	// Entries returns every stored (ChunkID, Vector) for the collection, in
	// unspecified order, as copies the caller may retain. An unknown collection
	// yields no entries and no error (mirrors Search). It feeds a collection's
	// own vectors back as queries (query --from-collection) without re-embedding.
	Entries(ctx context.Context, collection string) ([]VectorEntry, error)
	Delete(ctx context.Context, collection string, ids []domain.ChunkID) error
}

// LexicalDoc is one chunk's lexical content for the LexicalIndex: its identity,
// the text to index, and the document metadata a --where filter applies to. It is
// the lexical-side sibling of VectorEntry.
type LexicalDoc struct {
	ChunkID  domain.ChunkID
	Text     string
	Metadata domain.Metadata
}

// LexicalIndex is a keyword (BM25) index over chunk text — the lexical half of
// hybrid retrieval, a sibling of VectorIndex kept separate so each port stays
// small and every adapter implements it honestly (memstore in-memory BM25, sqlite
// FTS5). Results feed rank-based fusion (domain.FuseRRF) with the vector results,
// so Search returns only the ranked identities, not scores.
//
// Semantics:
//   - Upsert replaces entries with the same ChunkID.
//   - Search returns up to k chunk IDs ranked by lexical relevance, best first,
//     considering only entries whose Metadata satisfies filter. An empty query,
//     unknown collection, or k <= 0 yields no matches, no error. A chunk is a
//     candidate when it contains at least one query term.
//   - Delete of absent IDs is a no-op.
type LexicalIndex interface {
	Upsert(ctx context.Context, collection string, docs []LexicalDoc) error
	Search(ctx context.Context, collection string, query string, k int, filter domain.Predicate) ([]domain.ChunkID, error)
	Delete(ctx context.Context, collection string, ids []domain.ChunkID) error
}

// Embedder turns texts into vectors and reports the space it produces,
// so use cases can enforce space coherence against the target collection.
type Embedder interface {
	Space(ctx context.Context) (domain.EmbeddingSpace, error)
	// Embed returns one vector per input text, in input order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Answer is a grounded synthesis result with citations back to chunks, each
// carrying the source provenance needed to display it.
type Answer struct {
	Text      string
	Citations []domain.Citation
	// Grounded reports whether the answer had any grounding input — at least one
	// retrieved chunk or attachment. False means the model answered from its own
	// knowledge alone (only possible in non-strict mode).
	Grounded bool
}

// Generator synthesizes an answer grounded in retrieved chunks, optionally with
// raw attachments (images/documents) the model reads directly. Attachments are
// ephemeral context, not part of the collection.
type Generator interface {
	Synthesize(ctx context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment) (Answer, error)
}

// StreamingGenerator is an optional capability a Generator may implement: it
// emits the answer's prose incrementally via onDelta (called in order, once per
// token or chunk) while still returning the complete Answer — the full text and
// citations a caller needs for the post-answer sources, --json, and caching. A
// generator that cannot stream a given request (structured-output mode, or a
// cache hit) may call onDelta once with the whole text. Callers detect support
// with a type assertion; the CLI uses it only for interactive streaming.
type StreamingGenerator interface {
	SynthesizeStream(ctx context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment, onDelta func(string)) (Answer, error)
}

// AnswerCache stores synthesized answers keyed by an opaque content hash, for
// reuse across runs (each lore invocation is a fresh process, so this must be
// persistent to pay off). It is deliberately time-explicit: the TTL policy lives
// in the caller, not the store, which keeps the store a dumb time-indexed KV and
// the conformance suite deterministic.
//
// Semantics:
//   - Get returns a hit only when an entry exists AND was stored at or after
//     notBefore; an older entry is a miss (expired), as is an unknown key.
//   - Put replaces any existing entry for key.
//   - Prune deletes every entry stored before cutoff.
type AnswerCache interface {
	Get(ctx context.Context, key string, notBefore time.Time) (Answer, bool, error)
	Put(ctx context.Context, key string, answer Answer, storedAt time.Time) error
	Prune(ctx context.Context, cutoff time.Time) error
}

// SourceItem is one raw document yielded by a Source. Content is read lazily via
// Open so a consumer can decide — from the cheap Fingerprint — not to read at
// all. Fingerprint is a source-side signature (size + sampled-content hash) that
// changes when the file changes; an empty Fingerprint disables fast-skip.
type SourceItem struct {
	URI         string
	ContentType string
	Fingerprint string
	Open        func() ([]byte, error)
}

// Source yields raw documents from somewhere (filesystem walk, stdin, URL).
// Walk stops and returns the first error from fn, or ctx.Err on cancellation.
type Source interface {
	Walk(ctx context.Context, root string, fn func(SourceItem) error) error
}

// Extractor converts raw content of a supported type into plain text.
type Extractor interface {
	Supports(contentType string) bool
	Extract(contentType string, raw []byte) (string, error)
}

// TokenCounter approximates how many tokens a piece of text occupies in an
// embedding/generation model's context window. It backs the entire
// token-awareness story: chunk sizing (the domain chunkers take its Count as an
// injected func, keeping the tokenizer dependency out of stdlib-only domain) and
// budget-bounded retrieval. Counts are approximate-but-stable across models, not
// exact per-model — enough to size windows predictably.
type TokenCounter interface {
	Count(text string) int
}
