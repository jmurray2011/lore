package app

import (
	"context"
	"fmt"
	"path"
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

// sourceOverfetch multiplies k when a source filter is set, so enough candidates
// are retrieved for the filter to fill k. It is best-effort: if matching
// documents are buried below the over-fetch window (one document dominates the
// top results), some matches can still be missed.
const sourceOverfetch = 10

// Query embeds the question and returns up to k ChunkHits from the collection,
// best match first. It enforces space coherence (invariant 1): the embedder
// must produce vectors in the collection's space, or it fails with
// ErrSpaceMismatch before touching the index.
//
// A non-empty source restricts hits to documents whose source URI matches that
// glob (matched against the basename, or the path when the pattern contains a
// "/"); Query over-fetches to fill k, approximately (see sourceOverfetch).
//
// An empty query is ErrInvalidArgument; an unknown collection is ErrNotFound.
// No matching chunks yields no hits and no error.
func (q *Querier) Query(ctx context.Context, collection, query string, k int, source string) ([]domain.ChunkHit, error) {
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

	searchK := k
	if source != "" && k > 0 {
		searchK = k * sourceOverfetch
	}
	matches, err := q.index.Search(ctx, coll.Name, vecs[0], searchK)
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

	if source != "" {
		kept := hits[:0]
		for _, h := range hits {
			if matchSource(source, h.Source) {
				kept = append(kept, h)
			}
		}
		hits = kept
		if k > 0 && len(hits) > k {
			hits = hits[:k]
		}
	}
	return hits, nil
}

// matchSource reports whether a source URI matches the user's glob. A pattern
// without "/" matches the document basename (so "*.pdf" or "SSP*" work as
// expected); a pattern containing "/" matches the URI's path. A malformed
// pattern matches nothing.
func matchSource(pattern, sourceURI string) bool {
	p := sourceURI
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	candidate := p
	if !strings.Contains(pattern, "/") {
		candidate = path.Base(p)
	}
	ok, err := path.Match(pattern, candidate)
	return err == nil && ok
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
