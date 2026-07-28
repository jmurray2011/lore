package domain

import (
	"fmt"
	"strings"
	"time"
)

// ChunkID is deterministic, derived from (DocumentID, Seq), so re-chunking an
// unchanged document yields identical identities, keeping ingestion idempotent.
type ChunkID string

// Valid reports whether id has the canonical shape produced by DeriveChunkID: a
// 64-character lowercase-hex document hash, a colon, and a non-negative sequence
// number (e.g. "3f2a…9c:5"). It is a format check, not an existence check.
func (id ChunkID) Valid() bool {
	hash, seq, ok := strings.Cut(string(id), ":")
	if !ok || len(hash) != 64 || seq == "" {
		return false
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	for i := 0; i < len(seq); i++ {
		if seq[i] < '0' || seq[i] > '9' {
			return false
		}
	}
	return true
}

// Chunk is the unit of retrieval. It belongs to exactly one Document
// (deleting the Document deletes its Chunks).
type Chunk struct {
	ID         ChunkID
	DocumentID DocumentID
	Seq        int
	Text       string
	// HeadingPath is the document section a chunk came from, joined with " > "
	// (e.g. "Auth > Keys > Rotation"). Set by structure-aware chunkers; empty for
	// formats or strategies without headings. It is provenance for display and
	// inspection, not part of the chunk's identity.
	HeadingPath string
}

// NewChunk constructs a Chunk with its deterministic ID.
func NewChunk(docID DocumentID, seq int, text string) (Chunk, error) {
	if docID == "" {
		return Chunk{}, fmt.Errorf("chunk: %w: document ID is required", ErrInvalidArgument)
	}
	if seq < 0 {
		return Chunk{}, fmt.Errorf("chunk: %w: seq must be non-negative, got %d", ErrInvalidArgument, seq)
	}
	if text == "" {
		return Chunk{}, fmt.Errorf("chunk %s/%d: %w: text must not be empty", docID, seq, ErrInvalidArgument)
	}
	return Chunk{ID: DeriveChunkID(docID, seq), DocumentID: docID, Seq: seq, Text: text}, nil
}

// DeriveChunkID computes the deterministic identity of a chunk.
func DeriveChunkID(docID DocumentID, seq int) ChunkID {
	return ChunkID(fmt.Sprintf("%s:%d", docID, seq))
}

// VectorMatch is what a VectorIndex returns: an identity and a similarity
// score (higher is more similar), not yet hydrated with chunk content.
type VectorMatch struct {
	ChunkID ChunkID
	Score   float64
}

// ChunkHit is a hydrated retrieval result: the chunk itself, its similarity
// score, and the URI of the source document it came from (provenance for display
// and citation). RerankScore is set only after a cross-encoder rerank — nil
// means the hit was never reranked, so it serializes additively (rerank_score
// omitted) and the original Score is always preserved alongside it.
type ChunkHit struct {
	Chunk  Chunk
	Score  float64
	Source string // source URI of the chunk's document
	// Collection names the collection the chunk came from. It is empty for
	// single-collection retrieval (where it is implied by the request) and set
	// only for cross-collection results (multi-collection query/ask), so the
	// merged hits stay attributable to their origin.
	Collection  string
	RerankScore *float64
	// Metadata is the chunk's document-level attributes, attached at hydration for
	// display (--json, human output). Empty unless the document carried metadata.
	Metadata Metadata
	// IngestedAt is when the chunk's document was ingested, attached at hydration.
	// It is the recency fallback (see HitTime) when the document carries no date
	// metadata; the zero value means unknown.
	IngestedAt time.Time
}

// Citation references a chunk an answer was grounded in, carrying the provenance
// needed to display it: the source document's URI and the chunk's position
// within it.
type Citation struct {
	ChunkID ChunkID
	Source  string // source URI of the chunk's document
	Seq     int    // the chunk's position within that document
	// Collection names the chunk's collection, set only for cross-collection
	// answers (multi-collection ask) so a citation stays attributable to its
	// origin; empty for single-collection answers.
	Collection string
}
