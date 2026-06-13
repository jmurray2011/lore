package domain

import (
	"fmt"
	"strings"
)

// ChunkID is deterministic, derived from (DocumentID, Seq), so re-chunking an
// unchanged document yields identical identities (supports invariant 2).
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
// (invariant 3: deleting the Document deletes its Chunks).
type Chunk struct {
	ID         ChunkID
	DocumentID DocumentID
	Seq        int
	Text       string
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

// ChunkHit is a hydrated retrieval result: the chunk itself, its score, and the
// URI of the source document it came from (provenance for display and citation).
type ChunkHit struct {
	Chunk  Chunk
	Score  float64
	Source string // source URI of the chunk's document
}

// Citation references a chunk an answer was grounded in, carrying the provenance
// needed to display it: the source document's URI and the chunk's position
// within it.
type Citation struct {
	ChunkID ChunkID
	Source  string // source URI of the chunk's document
	Seq     int    // the chunk's position within that document
}
