package memstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// DocumentRepository is a thread-safe, in-memory store of Documents and their
// Chunks. Chunk data lives in byChunkID (the source of truth for GetChunks);
// docChunks tracks each document's chunk IDs so Upsert can replace and Delete
// can cascade (invariant 3). Values are stored and returned by copy.
type DocumentRepository struct {
	mu        sync.RWMutex
	docs      map[domain.DocumentID]domain.Document
	docChunks map[domain.DocumentID][]domain.ChunkID
	byChunkID map[domain.ChunkID]domain.Chunk
}

// compile-time port check
var _ app.DocumentRepository = (*DocumentRepository)(nil)

// NewDocumentRepository returns an empty repository.
func NewDocumentRepository() *DocumentRepository {
	return &DocumentRepository{
		docs:      make(map[domain.DocumentID]domain.Document),
		docChunks: make(map[domain.DocumentID][]domain.ChunkID),
		byChunkID: make(map[domain.ChunkID]domain.Chunk),
	}
}

// Upsert stores the document and replaces its chunks: any chunks the document
// previously had are dropped before the new ones are recorded.
func (r *DocumentRepository) Upsert(_ context.Context, doc *domain.Document, chunks []domain.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.docChunks[doc.ID] {
		delete(r.byChunkID, id)
	}

	r.docs[doc.ID] = *doc
	ids := make([]domain.ChunkID, len(chunks))
	for i, c := range chunks {
		ids[i] = c.ID
		r.byChunkID[c.ID] = c
	}
	r.docChunks[doc.ID] = ids
	return nil
}

// GetBySource returns a copy of the document for (collection, sourceURI), or
// ErrNotFound.
func (r *DocumentRepository) GetBySource(_ context.Context, collection, sourceURI string) (*domain.Document, error) {
	id := domain.DeriveDocumentID(collection, sourceURI)

	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.docs[id]
	if !ok {
		return nil, fmt.Errorf("document %q in collection %q: %w", sourceURI, collection, app.ErrNotFound)
	}
	return &d, nil
}

// GetChunks hydrates chunks by ID, preserving input order and skipping IDs with
// no stored chunk (the result may be shorter than the input).
func (r *DocumentRepository) GetChunks(_ context.Context, ids []domain.ChunkID) ([]domain.Chunk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]domain.Chunk, 0, len(ids))
	for _, id := range ids {
		if c, ok := r.byChunkID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// Delete removes the document and its chunks, or fails with ErrNotFound. Their
// vectors live in the VectorIndex and are removed by the use case.
func (r *DocumentRepository) Delete(_ context.Context, collection string, id domain.DocumentID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.docs[id]
	if !ok || d.Collection != collection {
		return fmt.Errorf("document %q in collection %q: %w", id, collection, app.ErrNotFound)
	}
	for _, cid := range r.docChunks[id] {
		delete(r.byChunkID, cid)
	}
	delete(r.docChunks, id)
	delete(r.docs, id)
	return nil
}
