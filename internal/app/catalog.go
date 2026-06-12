package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

// Catalog is the collection-management use case: create, list, and inspect the
// corpora lore knows about — including the documents ingested into a collection.
type Catalog struct {
	collections CollectionRepository
	docs        DocumentRepository
	embedder    Embedder
	now         func() time.Time
}

// NewCatalog wires a Catalog over the collection and document repositories and
// the embedder whose space new collections are pinned to.
func NewCatalog(collections CollectionRepository, docs DocumentRepository, embedder Embedder) *Catalog {
	return &Catalog{collections: collections, docs: docs, embedder: embedder, now: time.Now}
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

// ListDocuments returns the documents ingested into the named collection. It
// fails with ErrNotFound if the collection does not exist, so callers can
// distinguish an empty collection from a missing one.
func (c *Catalog) ListDocuments(ctx context.Context, collection string) ([]*domain.Document, error) {
	if _, err := c.collections.Get(ctx, collection); err != nil {
		return nil, err
	}
	return c.docs.ListDocuments(ctx, collection)
}
