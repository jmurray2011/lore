package domain

import (
	"fmt"
	"strings"
)

// Default chunking parameters (DESIGN.md: ~512 tokens, ~15% overlap). For the
// fixed strategy a "token" is a whitespace word; the structure-aware strategies
// measure real tokens via an injected counter.
const (
	DefaultChunkSize    = 512
	DefaultChunkOverlap = 76
)

// FixedChunkerVersion is the algorithm version of the fixed (word-window)
// strategy, recorded in a Collection's ChunkerSpec. Bump it if the fixed
// chunker's output could change for the same input.
const FixedChunkerVersion = 1

// ParsedDoc is the input to a Chunker: the extracted text of one document plus
// the context a strategy may need. ContentType lets the Registry dispatch to a
// per-format strategy; SourceURI is carried for diagnostics.
type ParsedDoc struct {
	Text        string
	ContentType string
	SourceURI   string
}

// ChunkResult is one chunk a Chunker produced: the text to store and cite, plus
// any section metadata. Identity (ChunkID, Seq) is assigned by the ingestor, not
// the chunker, since only the ingestor knows the owning Document.
type ChunkResult struct {
	Text string
	// EmbedText is the text to embed when it should differ from the stored Text —
	// e.g. a heading-path context prefix prepended so the embedding captures the
	// chunk's place in the document. Empty means embed Text as-is; either way the
	// stored Text (what citations and inspection show) is the original, never the
	// prefixed form.
	EmbedText string
	// HeadingPath is the document section the chunk came from (e.g.
	// "Auth > Keys > Rotation"), set by structure-aware chunkers; empty otherwise.
	HeadingPath string
}

// TextToEmbed returns the text that should be embedded for this chunk: the
// EmbedText override when set, otherwise the stored Text.
func (r ChunkResult) TextToEmbed() string {
	if r.EmbedText != "" {
		return r.EmbedText
	}
	return r.Text
}

// Chunker splits a parsed document into ordered chunks. Implementations are
// deterministic and pure (DESIGN.md decision 7): same input → identical output,
// no I/O, no map-iteration-order dependence. A token-aware strategy receives a
// token-count func at construction rather than importing a tokenizer, so
// stdlib-only domain stays clean.
type Chunker interface {
	Chunk(doc ParsedDoc) ([]ChunkResult, error)
}

// Registry selects a Chunker by content type, falling back to a default. It is
// how per-format strategies (markdown, plain text, ...) plug in without the
// ingestor knowing which is which; a future code-aware strategy is just another
// registered entry (revisit decision 7 then — a tree-sitter strategy cannot be
// pure-domain). It also carries the ChunkerSpec the active configuration
// corresponds to, so the use cases can pin it on new collections and refuse
// re-ingest under a different one. Construct with NewRegistry; the zero value is
// not usable.
type Registry struct {
	byType map[string]Chunker
	def    Chunker
	spec   ChunkerSpec
}

// NewRegistry builds a Registry routing each content type in byType to its
// Chunker and everything else to def, identified by spec. It requires a valid
// spec and a non-nil default, and rejects nil entries; content-type keys are
// normalized so dispatch is case- and parameter-insensitive.
func NewRegistry(spec ChunkerSpec, def Chunker, byType map[string]Chunker) (Registry, error) {
	if err := spec.Validate(); err != nil {
		return Registry{}, err
	}
	if def == nil {
		return Registry{}, fmt.Errorf("chunker registry: %w: a default chunker is required", ErrInvalidArgument)
	}
	m := make(map[string]Chunker, len(byType))
	for ct, c := range byType {
		if c == nil {
			return Registry{}, fmt.Errorf("chunker registry: %w: nil chunker for %q", ErrInvalidArgument, ct)
		}
		m[normalizeContentType(ct)] = c
	}
	return Registry{byType: m, def: def, spec: spec}, nil
}

// Spec returns the ChunkerSpec the registry's active configuration corresponds
// to — what new collections are pinned to and re-ingest is checked against.
func (r Registry) Spec() ChunkerSpec { return r.spec }

// Chunk dispatches to the chunker registered for the document's content type, or
// the default if none matches.
func (r Registry) Chunk(doc ParsedDoc) ([]ChunkResult, error) {
	c := r.def
	if specific, ok := r.byType[normalizeContentType(doc.ContentType)]; ok {
		c = specific
	}
	return c.Chunk(doc)
}

// normalizeContentType drops any "; charset=..." parameter and lower-cases the
// media type so dispatch is case- and parameter-insensitive.
func normalizeContentType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(strings.ToLower(ct))
}

// FixedChunker emits fixed-size, overlapping windows measured in whitespace
// words — the legacy chunking behavior, preserved verbatim as the `fixed`
// strategy and the default fallback. Word sizing is dependency-free and
// deterministic; the structure-aware strategies use real token counts instead.
// Chunk text is whitespace-normalized as a side effect.
//
// Construct with NewFixedChunker; the zero value is not usable.
type FixedChunker struct {
	size    int
	overlap int
}

// compile-time interface check
var _ Chunker = FixedChunker{}

// NewFixedChunker returns a FixedChunker that emits chunks of at most size words,
// with consecutive chunks sharing overlap words. It requires size > 0 and
// 0 <= overlap < size, so every window advances.
func NewFixedChunker(size, overlap int) (FixedChunker, error) {
	if size <= 0 {
		return FixedChunker{}, fmt.Errorf("chunker: %w: size must be positive, got %d", ErrInvalidArgument, size)
	}
	if overlap < 0 {
		return FixedChunker{}, fmt.Errorf("chunker: %w: overlap must be non-negative, got %d", ErrInvalidArgument, overlap)
	}
	if overlap >= size {
		return FixedChunker{}, fmt.Errorf("chunker: %w: overlap (%d) must be less than size (%d)", ErrInvalidArgument, overlap, size)
	}
	return FixedChunker{size: size, overlap: overlap}, nil
}

// Chunk implements Chunker by windowing the document text. ContentType is
// ignored — fixed sizing is format-agnostic.
func (c FixedChunker) Chunk(doc ParsedDoc) ([]ChunkResult, error) {
	texts := c.Split(doc.Text)
	results := make([]ChunkResult, len(texts))
	for i, t := range texts {
		results[i] = ChunkResult{Text: t}
	}
	return results, nil
}

// Split divides text into chunks in document order. Consecutive chunks share up
// to overlap words. Empty or whitespace-only input yields no chunks, and no
// chunk is ever empty.
func (c FixedChunker) Split(text string) []string {
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
