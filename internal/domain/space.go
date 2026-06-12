package domain

import "fmt"

// EmbeddingSpace identifies the vector space a Collection is bound to:
// the embedding model plus its output dimensionality. Two vectors are
// comparable only if they come from the same space (invariant 1, DESIGN.md).
type EmbeddingSpace struct {
	Model      string
	Dimensions int
}

// NewEmbeddingSpace validates and constructs an EmbeddingSpace.
func NewEmbeddingSpace(model string, dimensions int) (EmbeddingSpace, error) {
	if model == "" {
		return EmbeddingSpace{}, fmt.Errorf("embedding space: %w: model must not be empty", ErrInvalidArgument)
	}
	if dimensions <= 0 {
		return EmbeddingSpace{}, fmt.Errorf("embedding space: %w: dimensions must be positive, got %d", ErrInvalidArgument, dimensions)
	}
	return EmbeddingSpace{Model: model, Dimensions: dimensions}, nil
}

// Equal reports whether two spaces are the same (and therefore whether
// their vectors are comparable).
func (s EmbeddingSpace) Equal(other EmbeddingSpace) bool { return s == other }

// IsZero reports whether the space is uninitialized.
func (s EmbeddingSpace) IsZero() bool { return s == EmbeddingSpace{} }

func (s EmbeddingSpace) String() string {
	return fmt.Sprintf("%s/%d", s.Model, s.Dimensions)
}
