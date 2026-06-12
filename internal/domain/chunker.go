package domain

import (
	"fmt"
	"strings"
)

// Default chunking parameters (DESIGN.md: ~512 tokens, ~15% overlap). Sizes are
// in approximate tokens; see Chunker for what "token" means here.
const (
	DefaultChunkSize    = 512
	DefaultChunkOverlap = 76
)

// Chunker splits text into overlapping, size-bounded chunks — the retrieval
// units of a Document. It is a pure domain service: deterministic, no I/O
// (DESIGN.md decision 7).
//
// "Token" is approximated as a whitespace-delimited word: dependency-free and
// deterministic, which is enough to size retrieval windows. A precise,
// model-specific tokenizer can replace the estimate later without changing this
// type's contract. Chunk text is whitespace-normalized as a side effect.
//
// Construct with NewChunker; the zero value is not usable.
type Chunker struct {
	size    int
	overlap int
}

// NewChunker returns a Chunker that emits chunks of at most size tokens, with
// consecutive chunks sharing overlap tokens. It requires size > 0 and
// 0 <= overlap < size, so every window advances.
func NewChunker(size, overlap int) (Chunker, error) {
	if size <= 0 {
		return Chunker{}, fmt.Errorf("chunker: %w: size must be positive, got %d", ErrInvalidArgument, size)
	}
	if overlap < 0 {
		return Chunker{}, fmt.Errorf("chunker: %w: overlap must be non-negative, got %d", ErrInvalidArgument, overlap)
	}
	if overlap >= size {
		return Chunker{}, fmt.Errorf("chunker: %w: overlap (%d) must be less than size (%d)", ErrInvalidArgument, overlap, size)
	}
	return Chunker{size: size, overlap: overlap}, nil
}

// Split divides text into chunks in document order. Consecutive chunks share up
// to overlap tokens. Empty or whitespace-only input yields no chunks, and no
// chunk is ever empty.
func (c Chunker) Split(text string) []string {
	if c.size <= 0 { // unconfigured zero value: nothing sensible to do
		return nil
	}
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return nil
	}

	step := c.size - c.overlap
	var chunks []string
	for start := 0; start < len(tokens); start += step {
		end := start + c.size
		if end > len(tokens) {
			end = len(tokens)
		}
		chunks = append(chunks, strings.Join(tokens[start:end], " "))
		if end == len(tokens) {
			break
		}
	}
	return chunks
}
