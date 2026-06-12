package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

// Catalog is the collection-management use case: create, list, and inspect the
// corpora lore knows about. (Removal, with its cross-port cascade, joins here
// when the rm command lands.)
type Catalog struct {
	collections CollectionRepository
	embedder    Embedder
	now         func() time.Time
}

// NewCatalog wires a Catalog over the collection repository and the embedder
// whose space new collections are pinned to.
func NewCatalog(collections CollectionRepository, embedder Embedder) *Catalog {
	return &Catalog{collections: collections, embedder: embedder, now: time.Now}
}

// Init creates a collection pinned to the embedder's current EmbeddingSpace
// (invariant 1). It fails with ErrAlreadyExists if the name is taken and
// ErrInvalidArgument if the name is invalid.
func (c *Catalog) Init(ctx context.Context, name string) (*domain.Collection, error) {
	space, err := c.embedder.Space(ctx)
	if err != nil {
		return nil, fmt.Errorf("embedder space: %w", err)
	}
	coll, err := domain.NewCollection(name, space, c.now())
	if err != nil {
		return nil, err
	}
	if err := c.collections.Create(ctx, coll); err != nil {
		return nil, err
	}
	return coll, nil
}

// List returns every collection.
func (c *Catalog) List(ctx context.Context) ([]*domain.Collection, error) {
	return c.collections.List(ctx)
}

// Get returns the named collection, or ErrNotFound.
func (c *Catalog) Get(ctx context.Context, name string) (*domain.Collection, error) {
	return c.collections.Get(ctx, name)
}
