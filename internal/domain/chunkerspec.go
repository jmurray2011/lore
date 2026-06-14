package domain

import (
	"fmt"
	"strings"
)

// ChunkerSpec identifies the chunking configuration a Collection is pinned to:
// the strategy, its algorithm version, the size/overlap parameters, the
// tokenizer used for sizing, and whether a heading-path context prefix is
// embedded. Any of these changing alters chunk output (boundaries, count, or
// embedded text) for the same input, so a Collection records its spec at
// creation and refuses re-ingestion under a different one — the chunker
// analogue of space coherence. It is comparable with ==.
type ChunkerSpec struct {
	Strategy      string // "structure" | "fixed"
	Version       int    // strategy algorithm version; bump when output could change for the same input
	Size          int    // target chunk size (tokens for structure, words for fixed)
	Overlap       int    // overlap between adjacent chunks, same unit as Size
	Tokenizer     string // sizing tokenizer identity, e.g. "o200k_base" (structure) or "words" (fixed)
	ContextPrefix bool   // whether a heading-path context line is prepended to the embedded text
}

// NewChunkerSpec validates and constructs a ChunkerSpec.
func NewChunkerSpec(strategy string, version, size, overlap int, tokenizer string, contextPrefix bool) (ChunkerSpec, error) {
	s := ChunkerSpec{
		Strategy:      strategy,
		Version:       version,
		Size:          size,
		Overlap:       overlap,
		Tokenizer:     tokenizer,
		ContextPrefix: contextPrefix,
	}
	if err := s.Validate(); err != nil {
		return ChunkerSpec{}, err
	}
	return s, nil
}

// Validate reports whether the spec is well-formed.
func (s ChunkerSpec) Validate() error {
	switch {
	case s.Strategy == "":
		return fmt.Errorf("chunker spec: %w: strategy must not be empty", ErrInvalidArgument)
	case s.Version <= 0:
		return fmt.Errorf("chunker spec: %w: version must be positive, got %d", ErrInvalidArgument, s.Version)
	case s.Size <= 0:
		return fmt.Errorf("chunker spec: %w: size must be positive, got %d", ErrInvalidArgument, s.Size)
	case s.Overlap < 0:
		return fmt.Errorf("chunker spec: %w: overlap must be non-negative, got %d", ErrInvalidArgument, s.Overlap)
	case s.Overlap >= s.Size:
		return fmt.Errorf("chunker spec: %w: overlap (%d) must be less than size (%d)", ErrInvalidArgument, s.Overlap, s.Size)
	case s.Tokenizer == "":
		return fmt.Errorf("chunker spec: %w: tokenizer must not be empty", ErrInvalidArgument)
	}
	return nil
}

// IsZero reports whether the spec is uninitialized — the state of a Collection
// created before chunker pinning existed.
func (s ChunkerSpec) IsZero() bool { return s == ChunkerSpec{} }

// String renders the spec for human and error output, e.g.
// "structure/v1 size=512 overlap=64 tok=o200k_base prefix=on".
func (s ChunkerSpec) String() string {
	prefix := "off"
	if s.ContextPrefix {
		prefix = "on"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/v%d size=%d overlap=%d tok=%s prefix=%s",
		s.Strategy, s.Version, s.Size, s.Overlap, s.Tokenizer, prefix)
	return b.String()
}
