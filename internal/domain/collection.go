package domain

import (
	"fmt"
	"regexp"
	"time"
)

// collectionNameRE: non-empty, filesystem- and shell-safe, max 64 chars
// (invariant 4, DESIGN.md).
var collectionNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Collection is the aggregate root: a named corpus bound to exactly one
// EmbeddingSpace for its entire lifetime.
type Collection struct {
	Name      string
	Space     EmbeddingSpace
	CreatedAt time.Time
	// Sources are the paths ingested into this collection, recorded by add and
	// sync so `lore sync` can replay them without a path argument.
	Sources []string
}

// NewCollection validates identity and space and constructs a Collection.
// The clock is injected so the domain stays deterministic and testable.
func NewCollection(name string, space EmbeddingSpace, now time.Time) (*Collection, error) {
	if !collectionNameRE.MatchString(name) {
		return nil, fmt.Errorf("collection name %q: %w: must match %s", name, ErrInvalidArgument, collectionNameRE)
	}
	if space.IsZero() {
		return nil, fmt.Errorf("collection %q: %w: embedding space is required", name, ErrInvalidArgument)
	}
	return &Collection{Name: name, Space: space, CreatedAt: now}, nil
}

// AcceptsSpace enforces invariant 1 (space coherence): vectors may enter the
// collection only if they were produced in the collection's own space.
func (c *Collection) AcceptsSpace(s EmbeddingSpace) error {
	if !c.Space.Equal(s) {
		return fmt.Errorf("collection %q is bound to %s, got %s: %w", c.Name, c.Space, s, ErrSpaceMismatch)
	}
	return nil
}
