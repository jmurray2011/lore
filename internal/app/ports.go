// Package app contains the use cases and defines the ports (interfaces)
// they consume. Adapters implement these ports; the conformance suites in
// internal/conformance are their executable contracts.
package app

import (
	"context"
	"errors"

	"github.com/jmurray2011/lore/internal/domain"
)

// Sentinel errors shared across ports. Adapters wrap these with %w so
// callers can use errors.Is; the CLI maps them to exit codes (DESIGN.md).
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
	// (invariant 3) is orchestrated by the use case, which holds the
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
	// (invariant 3 — this port cannot reach it). Fails with ErrNotFound if no
	// such document exists in the collection.
	Delete(ctx context.Context, collection string, id domain.DocumentID) ([]domain.ChunkID, error)
	// DeleteCollection removes every document and its chunks in the collection,
	// returning all removed chunk IDs for the same cascade. A collection with no
	// documents is a no-op (no IDs, no error); collection existence is the
	// CollectionRepository's concern.
	DeleteCollection(ctx context.Context, collection string) ([]domain.ChunkID, error)
}

// VectorEntry pairs a chunk identity with its vector for indexing.
type VectorEntry struct {
	ChunkID domain.ChunkID
	Vector  []float32
}

// VectorIndex stores and searches vectors. It is deliberately dumb: space
// coherence (invariant 1) is enforced by the use cases via
// Collection.AcceptsSpace before anything reaches the index.
//
// Semantics (verified by conformance.RunVectorIndexSuite):
//   - Upsert replaces entries with the same ChunkID.
//   - Search returns up to k matches, best first (higher score = more
//     similar). Unknown collection or k <= 0 yields no matches, no error.
//   - Delete of absent IDs is a no-op.
type VectorIndex interface {
	Upsert(ctx context.Context, collection string, entries []VectorEntry) error
	Search(ctx context.Context, collection string, query []float32, k int) ([]domain.VectorMatch, error)
	Delete(ctx context.Context, collection string, ids []domain.ChunkID) error
}

// Embedder turns texts into vectors and reports the space it produces,
// so use cases can enforce invariant 1 against the target collection.
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
