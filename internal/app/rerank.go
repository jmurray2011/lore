package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/jmurray2011/lore/internal/domain"
)

// RankResult is one scored document from a RerankProvider: Index points into the
// documents slice the provider was given; Score is its relevance (higher = more
// relevant).
type RankResult struct {
	Index int
	Score float64
}

// RerankProvider scores (query, document) pairs jointly with a cross-encoder —
// the rerank half of two-stage retrieval. It is a distinct port from Embedder/
// Generator because rerank APIs are their own thing (Cohere-style /rerank), not
// the OpenAI chat/embeddings shape, and are usually a separate provider.
type RerankProvider interface {
	// Rerank scores documents against query and returns results best-first. topN
	// is a hint passed to providers that support it; the Reranker use case is the
	// source of truth for final ordering and truncation.
	Rerank(ctx context.Context, query string, documents []string, topN int) ([]RankResult, error)
}

// Reranker is the two-stage-retrieval use case: it reranks already-retrieved
// hits against the query via a RerankProvider, reordering them by relevance and
// attaching each hit's rerank score (the original similarity score is kept).
type Reranker struct {
	provider RerankProvider
}

// NewReranker wires a Reranker over a RerankProvider.
func NewReranker(provider RerankProvider) *Reranker {
	return &Reranker{provider: provider}
}

// Rerank reorders hits by cross-encoder relevance to query and returns them with
// RerankScore attached, best first. topN > 0 truncates to that many after
// reranking; topN <= 0 re-emits all hits reordered. Empty hits is a no-op (the
// provider is not called). The use case owns ordering and truncation, so the
// result is identical regardless of whether the provider honored topN.
func (r *Reranker) Rerank(ctx context.Context, query string, hits []domain.ChunkHit, topN int) ([]domain.ChunkHit, error) {
	if len(hits) == 0 {
		return nil, nil
	}

	documents := make([]string, len(hits))
	for i, h := range hits {
		documents[i] = h.Chunk.Text
	}

	results, err := r.provider.Rerank(ctx, query, documents, topN)
	if err != nil {
		return nil, err
	}

	out := make([]domain.ChunkHit, 0, len(results))
	for _, res := range results {
		if res.Index < 0 || res.Index >= len(hits) {
			return nil, fmt.Errorf("rerank: result index %d out of range [0,%d)", res.Index, len(hits))
		}
		h := hits[res.Index]
		score := res.Score
		h.RerankScore = &score
		out = append(out, h)
	}

	sort.SliceStable(out, func(i, j int) bool { return *out[i].RerankScore > *out[j].RerankScore })
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out, nil
}
