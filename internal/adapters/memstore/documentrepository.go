package memstore

import (
	"context"
	"fmt"
	"sort"
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

// GetChunksByDocument returns all chunks of one document in seq order. An
// unknown document yields no chunks and no error.
func (r *DocumentRepository) GetChunksByDocument(_ context.Context, collection string, id domain.DocumentID) ([]domain.Chunk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if d, ok := r.docs[id]; !ok || d.Collection != collection {
		return nil, nil
	}
	ids := r.docChunks[id]
	out := make([]domain.Chunk, 0, len(ids))
	for _, cid := range ids {
		if c, ok := r.byChunkID[cid]; ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// GetChunksByIDs returns the chunks with the given IDs that belong to the
// collection, in input order. IDs absent from the collection (unknown, or owned
// by another collection) are omitted. An unknown collection yields no chunks.
func (r *DocumentRepository) GetChunksByIDs(_ context.Context, collection string, ids []string) ([]domain.Chunk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]domain.Chunk, 0, len(ids))
	for _, id := range ids {
		c, ok := r.byChunkID[domain.ChunkID(id)]
		if !ok {
			continue
		}
		if d, ok := r.docs[c.DocumentID]; !ok || d.Collection != collection {
			continue // chunk belongs to another collection
		}
		out = append(out, c)
	}
	return out, nil
}

// GetDocuments hydrates documents by ID, preserving input order and skipping IDs
// with no stored document (the result may be shorter than the input).
func (r *DocumentRepository) GetDocuments(_ context.Context, ids []domain.DocumentID) ([]*domain.Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := r.docs[id]; ok {
			doc := d
			out = append(out, &doc)
		}
	}
	return out, nil
}

// ListDocuments returns every document in the collection (order unspecified). An
// unknown or empty collection yields no documents and no error.
func (r *DocumentRepository) ListDocuments(_ context.Context, collection string) ([]*domain.Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []*domain.Document
	for _, d := range r.docs {
		if d.Collection == collection {
			doc := d
			out = append(out, &doc)
		}
	}
	return out, nil
}

// Delete removes the document and its chunks, returning the removed chunk IDs,
// or fails with ErrNotFound. Their vectors live in the VectorIndex and are
// removed by the use case.
func (r *DocumentRepository) Delete(_ context.Context, collection string, id domain.DocumentID) ([]domain.ChunkID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.docs[id]
	if !ok || d.Collection != collection {
		return nil, fmt.Errorf("document %q in collection %q: %w", id, collection, app.ErrNotFound)
	}
	return r.deleteLocked(id), nil
}

// DeleteCollection removes every document and its chunks in the collection,
// returning all removed chunk IDs. An empty collection is a no-op.
func (r *DocumentRepository) DeleteCollection(_ context.Context, collection string) ([]domain.ChunkID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var removed []domain.ChunkID
	for id, d := range r.docs {
		if d.Collection == collection {
			removed = append(removed, r.deleteLocked(id)...)
		}
	}
	return removed, nil
}

// deleteLocked removes a document and its chunks, returning the removed chunk
// IDs. Callers must hold r.mu.
func (r *DocumentRepository) deleteLocked(id domain.DocumentID) []domain.ChunkID {
	ids := r.docChunks[id]
	for _, cid := range ids {
		delete(r.byChunkID, cid)
	}
	delete(r.docChunks, id)
	delete(r.docs, id)
	return ids
}
