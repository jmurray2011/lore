package domain

import "errors"

// Sentinel errors for domain invariant violations. Infrastructure-level
// errors (ErrNotFound etc.) live in internal/app, next to the ports.
var (
	// ErrInvalidArgument marks construction/validation failures of domain values.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrSpaceMismatch marks a violation of invariant 1 (space coherence):
	// an attempt to mix vectors from a different EmbeddingSpace into a Collection.
	ErrSpaceMismatch = errors.New("embedding space mismatch")

	// ErrChunkerMismatch marks an attempt to (re-)ingest into a Collection with a
	// chunker different from the one it was pinned to (or any chunker, for a
	// collection that predates pinning). Like a space mismatch it is an invariant
	// violation; the CLI maps it to exit 4.
	ErrChunkerMismatch = errors.New("chunker mismatch")
)
