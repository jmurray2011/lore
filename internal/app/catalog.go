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
	chunkers    domain.Registry
	now         func() time.Time
}

// NewCatalog wires a Catalog over the collection and document repositories, the
// embedder whose space new collections are pinned to, and the chunker registry
// whose spec they are pinned to.
func NewCatalog(collections CollectionRepository, docs DocumentRepository, embedder Embedder, chunkers domain.Registry) *Catalog {
	return &Catalog{collections: collections, docs: docs, embedder: embedder, chunkers: chunkers, now: time.Now}
}

// Init creates a collection pinned to the embedder's current EmbeddingSpace
// and the active chunker spec (re-ingest under a different chunker
// is then refused). It fails with ErrAlreadyExists if the name is taken and
// ErrInvalidArgument if the name is invalid.
func (c *Catalog) Init(ctx context.Context, name string) (*domain.Collection, error) {
	space, err := c.embedder.Space(ctx)
	if err != nil {
		return nil, fmt.Errorf("embedder space: %w", err)
	}
	coll, err := domain.NewCollection(name, space, c.chunkers.Spec(), c.now())
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

// DocumentChunks returns the stored chunks of one document (by source URI) in
// seq order — the extracted, chunked text as it was actually indexed, for
// inspecting what the extractor and chunker produced. It fails with ErrNotFound
// if no document with that source URI exists in the collection.
func (c *Catalog) DocumentChunks(ctx context.Context, collection, sourceURI string) ([]domain.Chunk, error) {
	doc, err := c.docs.GetBySource(ctx, collection, sourceURI)
	if err != nil {
		return nil, err
	}
	return c.docs.GetChunksByDocument(ctx, collection, doc.ID)
}

// ChunksByIDs returns the collection's chunks with the given IDs, in input order,
// for inspecting specific or cited chunks. IDs not present are omitted (the
// caller diffs requested vs returned). It fails with ErrNotFound if the
// collection does not exist, so an unknown collection is distinguished from
// merely-absent chunk IDs.
func (c *Catalog) ChunksByIDs(ctx context.Context, collection string, ids []string) ([]domain.Chunk, error) {
	if _, err := c.collections.Get(ctx, collection); err != nil {
		return nil, err
	}
	return c.docs.GetChunksByIDs(ctx, collection, ids)
}
