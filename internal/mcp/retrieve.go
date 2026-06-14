package mcp

import (
	"context"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// These defaults mirror the CLI. The MCP tools take no rerank-candidate knob, so
// the candidate pool is max(defaultRerankCandidates, k) — never smaller than k.
const (
	defaultK                = 8
	defaultRerankCandidates = 50
	defaultHalfLifeDays     = 90
)

// retrieveParams are the retrieval levers shared by the ask and query tools,
// mirroring the CLI flags. They map straight onto app.RetrieveOptions.
type retrieveParams struct {
	K       int
	Source  string
	Where   []string
	Rerank  bool
	Hybrid  bool
	MMR     bool
	Recency bool
}

// resolveHits retrieves the hits for the ask/query tools through the shared
// Retriever — the one place retrieval composition lives (also used by the CLI and
// the eval harness). collections must be non-empty and de-duplicated by the caller.
func (s *Server) resolveHits(ctx context.Context, collections []string, query string, p retrieveParams) ([]domain.ChunkHit, error) {
	k := p.K
	if k <= 0 {
		k = defaultK
	}
	candidates := defaultRerankCandidates
	if k > candidates {
		candidates = k
	}
	filter, err := domain.ParseWhere(p.Where)
	if err != nil {
		return nil, err
	}
	hits, _, err := s.deps.Retriever.Resolve(ctx, app.RetrieveOptions{
		Collections: collections,
		Query:       query,
		K:           k,
		Candidates:  candidates,
		Source:      p.Source,
		Filter:      filter,
		Rerank:      p.Rerank,
		Hybrid:      p.Hybrid,
		MMR:         p.MMR,
		MMRLambda:   0.5,
		Recency:     p.Recency,
		HalfLife:    defaultHalfLifeDays * 24 * time.Hour,
	})
	return hits, err
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
