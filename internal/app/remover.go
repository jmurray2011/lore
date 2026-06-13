package app

import (
	"context"
	"fmt"

	"github.com/jmurray2011/lore/internal/domain"
)

// Remover owns the cross-port deletion cascade: a document's
// chunks live in the DocumentRepository, their vectors in the VectorIndex, and
// the collection record in the CollectionRepository. No single port can reach
// the others, so this use case orchestrates them.
type Remover struct {
	collections CollectionRepository
	docs        DocumentRepository
	index       VectorIndex
}

// NewRemover wires a Remover over the three persistence ports.
func NewRemover(collections CollectionRepository, docs DocumentRepository, index VectorIndex) *Remover {
	return &Remover{collections: collections, docs: docs, index: index}
}

// RemoveCollection deletes the collection and everything in it: its documents,
// their chunks, and their vectors. Fails with ErrNotFound if no such collection
// exists.
func (r *Remover) RemoveCollection(ctx context.Context, name string) error {
	if _, err := r.collections.Get(ctx, name); err != nil {
		return err
	}
	chunkIDs, err := r.docs.DeleteCollection(ctx, name)
	if err != nil {
		return fmt.Errorf("delete documents: %w", err)
	}
	if err := r.index.Delete(ctx, name, chunkIDs); err != nil {
		return fmt.Errorf("delete vectors: %w", err)
	}
	return r.collections.Delete(ctx, name)
}

// RemoveDocument deletes one document, its chunks, and their vectors. Fails with
// ErrNotFound if the document was never ingested into the collection.
func (r *Remover) RemoveDocument(ctx context.Context, collection, sourceURI string) error {
	id := domain.DeriveDocumentID(collection, sourceURI)
	chunkIDs, err := r.docs.Delete(ctx, collection, id)
	if err != nil {
		return err
	}
	if err := r.index.Delete(ctx, collection, chunkIDs); err != nil {
		return fmt.Errorf("delete vectors: %w", err)
	}
	return nil
}

// RemoveChunks deletes specific chunks by ID and their vectors, returning the
// IDs actually removed. This is sub-document redaction: the chunks' documents
// stay in place. IDs absent from the collection are skipped, so the caller can
// diff requested vs removed and report the misses. Fails with ErrNotFound if no
// such collection exists.
func (r *Remover) RemoveChunks(ctx context.Context, collection string, ids []domain.ChunkID) ([]domain.ChunkID, error) {
	if _, err := r.collections.Get(ctx, collection); err != nil {
		return nil, err
	}
	removed, err := r.docs.DeleteChunks(ctx, collection, ids)
	if err != nil {
		return nil, fmt.Errorf("delete chunks: %w", err)
	}
	if err := r.index.Delete(ctx, collection, removed); err != nil {
		return nil, fmt.Errorf("delete vectors: %w", err)
	}
	return removed, nil
}
