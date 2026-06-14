package mcp

import (
	"context"

	"github.com/jmurray2011/lore/internal/domain"
)

// These defaults mirror the CLI (internal/cli/retrieval.go). The MCP tools take
// no rerank-candidate knob, so the candidate pool is max(defaultRerankCandidates,
// k) — guaranteeing the pool is never smaller than the final k.
const (
	defaultK                = 8
	defaultRerankCandidates = 50
)

// resolveHits retrieves the hits for the ask/query tools, composing the same app
// use cases the CLI's resolveHits/queryHits do (no reimplementation of retrieval
// or rerank): a plain top-k vector search across one or more same-space
// collections, or — with rerank — a wide candidate pool reranked to the final
// top-k. collections must be non-empty and de-duplicated by the caller.
func (s *Server) resolveHits(ctx context.Context, collections []string, query string, k int, source string, rerank bool) ([]domain.ChunkHit, error) {
	if k <= 0 {
		k = defaultK
	}
	if rerank {
		if s.deps.Rerank == nil {
			return nil, errRerankUnconfigured()
		}
		candidates := defaultRerankCandidates
		if k > candidates {
			candidates = k
		}
		pool, err := s.queryHits(ctx, collections, query, candidates, source)
		if err != nil {
			return nil, err
		}
		return s.deps.Rerank.Rerank(ctx, query, pool, k)
	}
	return s.queryHits(ctx, collections, query, k, source)
}

// queryHits retrieves the top-k hits for one collection (the single-collection
// path) or merges across several same-space collections.
func (s *Server) queryHits(ctx context.Context, collections []string, query string, k int, source string) ([]domain.ChunkHit, error) {
	// The MCP tools do not yet expose a --where metadata filter, so pass the zero
	// predicate (matches everything). Adding a `where` tool param is a follow-up.
	if len(collections) > 1 {
		return s.deps.Query.QueryAcross(ctx, collections, query, k, source, domain.Predicate{})
	}
	return s.deps.Query.Query(ctx, collections[0], query, k, source, domain.Predicate{})
}

// budgetTrim limits ranked hits to a cumulative token budget, applied after
// ranking (and any rerank); it always keeps at least the first hit. budget <= 0
// (or no counter) is a no-op. Mirrors the CLI's budgetTrim.
func (s *Server) budgetTrim(hits []domain.ChunkHit, budget int) []domain.ChunkHit {
	if budget <= 0 || s.deps.Tokens == nil {
		return hits
	}
	var out []domain.ChunkHit
	total := 0
	for _, h := range hits {
		t := s.deps.Tokens.Count(h.Chunk.Text)
		if len(out) > 0 && total+t > budget {
			break
		}
		out = append(out, h)
		total += t
	}
	return out
}

// dedup removes duplicate strings, preserving first-seen order — so a repeated
// collection in the request routes through the single-collection path.
func dedup(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
