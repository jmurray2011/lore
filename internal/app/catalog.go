package app

import (
	"context"
	"fmt"
	"sort"
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

// CollectionDiff is the document-level difference between two collections,
// compared by source URI and content hash. It is space-independent: it compares
// what was ingested, not how it was embedded, so two collections pinned to
// different embedding spaces (e.g. a collection and a re-imported snapshot) can
// still be diffed. Each slice is ordered by source URI.
type CollectionDiff struct {
	Added   []DocRef    // present in `to`, absent from `from`
	Removed []DocRef    // present in `from`, absent from `to`
	Changed []DocChange // same source URI, different content hash
}

// Empty reports whether the two collections hold the same documents at the same
// content (no additions, removals, or changes).
func (d CollectionDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// DocRef identifies one document by its source URI and content hash.
type DocRef struct {
	SourceURI string
	Hash      string
}

// DocChange is one document whose content hash differs between the two
// collections (From in `from`, To in `to`).
type DocChange struct {
	SourceURI string
	From      string
	To        string
}

// Diff reports the document-level difference between two collections, comparing
// by source URI and content hash. It fails with ErrNotFound if either collection
// does not exist.
func (c *Catalog) Diff(ctx context.Context, from, to string) (CollectionDiff, error) {
	fromDocs, err := c.ListDocuments(ctx, from)
	if err != nil {
		return CollectionDiff{}, fmt.Errorf("from-collection %q: %w", from, err)
	}
	toDocs, err := c.ListDocuments(ctx, to)
	if err != nil {
		return CollectionDiff{}, fmt.Errorf("to-collection %q: %w", to, err)
	}

	fromHash := hashByURI(fromDocs)
	toHash := hashByURI(toDocs)

	var diff CollectionDiff
	for _, uri := range sortedURIs(fromHash, toHash) {
		f, inFrom := fromHash[uri]
		t, inTo := toHash[uri]
		switch {
		case inFrom && !inTo:
			diff.Removed = append(diff.Removed, DocRef{SourceURI: uri, Hash: string(f)})
		case !inFrom && inTo:
			diff.Added = append(diff.Added, DocRef{SourceURI: uri, Hash: string(t)})
		case f != t:
			diff.Changed = append(diff.Changed, DocChange{SourceURI: uri, From: string(f), To: string(t)})
		}
	}
	return diff, nil
}

// hashByURI indexes documents by source URI to their content hash.
func hashByURI(docs []*domain.Document) map[string]domain.ContentHash {
	m := make(map[string]domain.ContentHash, len(docs))
	for _, d := range docs {
		m[d.SourceURI] = d.Hash
	}
	return m
}

// sortedURIs returns the sorted union of keys across two URI-keyed maps, so the
// diff is deterministic regardless of repository iteration order.
func sortedURIs(a, b map[string]domain.ContentHash) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for uri := range a {
		seen[uri] = struct{}{}
	}
	for uri := range b {
		seen[uri] = struct{}{}
	}
	uris := make([]string, 0, len(seen))
	for uri := range seen {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	return uris
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
