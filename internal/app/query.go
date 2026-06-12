package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmurray2011/lore/internal/domain"
)

// Querier retrieves the chunks most similar to a query within a Collection.
type Querier struct {
	collections CollectionRepository
	index       VectorIndex
	docs        DocumentRepository
	embedder    Embedder
}

// NewQuerier wires a Querier from the ports it needs.
func NewQuerier(collections CollectionRepository, index VectorIndex, docs DocumentRepository, embedder Embedder) *Querier {
	return &Querier{collections: collections, index: index, docs: docs, embedder: embedder}
}

// Query embeds the question and returns up to k ChunkHits from the collection,
// best match first. It enforces space coherence (invariant 1): the embedder
// must produce vectors in the collection's space, or it fails with
// ErrSpaceMismatch before touching the index.
//
// An empty query is ErrInvalidArgument; an unknown collection is ErrNotFound.
// No matching chunks yields no hits and no error.
func (q *Querier) Query(ctx context.Context, collection, query string, k int) ([]domain.ChunkHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query: %w: text must not be empty", domain.ErrInvalidArgument)
	}

	coll, err := q.collections.Get(ctx, collection)
	if err != nil {
		return nil, err
	}

	space, err := q.embedder.Space(ctx)
	if err != nil {
		return nil, fmt.Errorf("embedder space: %w", err)
	}
	if err := coll.AcceptsSpace(space); err != nil {
		return nil, err
	}

	vecs, err := q.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for 1 query", len(vecs))
	}

	matches, err := q.index.Search(ctx, coll.Name, vecs[0], k)
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", coll.Name, err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	ids := make([]domain.ChunkID, len(matches))
	scoreByID := make(map[domain.ChunkID]float64, len(matches))
	for i, m := range matches {
		ids[i] = m.ChunkID
		scoreByID[m.ChunkID] = m.Score
	}

	chunks, err := q.docs.GetChunks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate chunks: %w", err)
	}

	sourceByDoc, err := q.sources(ctx, chunks)
	if err != nil {
		return nil, err
	}

	hits := make([]domain.ChunkHit, 0, len(chunks))
	for _, c := range chunks {
		hits = append(hits, domain.ChunkHit{Chunk: c, Score: scoreByID[c.ID], Source: sourceByDoc[c.DocumentID]})
	}
	return hits, nil
}

// sources hydrates the source URI of each chunk's document, returning a map from
// document ID to source URI. Documents that can't be hydrated are simply absent,
// leaving those hits with an empty Source rather than failing the whole query.
func (q *Querier) sources(ctx context.Context, chunks []domain.Chunk) (map[domain.DocumentID]string, error) {
	ids := make([]domain.DocumentID, 0, len(chunks))
	seen := make(map[domain.DocumentID]bool, len(chunks))
	for _, c := range chunks {
		if !seen[c.DocumentID] {
			seen[c.DocumentID] = true
			ids = append(ids, c.DocumentID)
		}
	}

	docs, err := q.docs.GetDocuments(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate sources: %w", err)
	}
	byDoc := make(map[domain.DocumentID]string, len(docs))
	for _, d := range docs {
		byDoc[d.ID] = d.SourceURI
	}
	return byDoc, nil
}
