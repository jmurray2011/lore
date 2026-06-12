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
)
