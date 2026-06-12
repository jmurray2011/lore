package domain

import "fmt"

// ChunkID is deterministic, derived from (DocumentID, Seq), so re-chunking an
// unchanged document yields identical identities (supports invariant 2).
type ChunkID string

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

// ChunkHit is a hydrated retrieval result: the chunk itself plus its score.
type ChunkHit struct {
	Chunk Chunk
	Score float64
}
