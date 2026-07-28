package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

// poolMin is the minimum candidate pool fetched before a reordering pass (MMR or
// recency) selects the final top-k, so a strong-but-not-top-k chunk can surface.
const poolMin = 50

// RetrieveOptions configures Retriever.Resolve — the single source of truth for
// retrieval composition shared by the CLI, the eval harness, and the MCP server.
// Rerank, MMR, and Recency each reorder the candidate pool and are mutually
// exclusive. Hybrid, Source, Filter, and MaxPerSource compose with any mode.
type RetrieveOptions struct {
	Collections []string
	Query       string
	K           int
	Candidates  int // rerank candidate pool (must be >= K when Rerank)
	Source      string
	Filter      domain.Predicate
	Rerank      bool
	Explain     bool // surface the runner-up just outside the top-k
	Hybrid      bool
	// Lexical selects BM25-only retrieval (no query embedding), for querying with
	// no usable embedder. It is a distinct mode, mutually exclusive with the
	// vector-based reorderings (Hybrid/Rerank/MMR/Recency) and single-collection.
	Lexical      bool
	MMR          bool
	MMRLambda    float64
	Recency      bool
	HalfLife     time.Duration
	MaxPerSource int // 0 = no cap
}

// Retriever resolves a query into ranked hits, composing vector search with
// optional hybrid fusion, cross-encoder rerank, MMR diversification, recency
// decay, and a per-source cap. It is the one place that composition lives;
// drivers (CLI/eval/MCP) translate their flags into RetrieveOptions and call it.
type Retriever struct {
	querier *Querier
	rerank  *Reranker   // nil when no rerank provider is configured
	index   VectorIndex // for MMR candidate vectors (Entries)
	now     func() time.Time
}

// NewRetriever wires a Retriever. rerank may be nil (a Rerank request then errors).
func NewRetriever(querier *Querier, rerank *Reranker, index VectorIndex) *Retriever {
	return &Retriever{querier: querier, rerank: rerank, index: index, now: time.Now}
}

// Resolve runs the retrieval pipeline for opts and returns the ranked hits plus,
// when Explain is set, the runner-up score (the best candidate just outside the
// returned set). The composition order is rerank/MMR/recency → per-source cap.
func (r *Retriever) Resolve(ctx context.Context, opts RetrieveOptions) ([]domain.ChunkHit, *float64, error) {
	hits, runnerUp, err := r.rank(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return domain.CapPerSource(hits, opts.MaxPerSource), runnerUp, nil
}

// rank selects and runs the appropriate reordering strategy.
func (r *Retriever) rank(ctx context.Context, opts RetrieveOptions) ([]domain.ChunkHit, *float64, error) {
	switch {
	case opts.Lexical:
		return r.lexical(ctx, opts)
	case opts.Recency:
		if opts.MMR {
			return nil, nil, fmt.Errorf("%w: recency and MMR are mutually exclusive (both reorder the candidate pool)", domain.ErrInvalidArgument)
		}
		return r.recency(ctx, opts)
	case opts.MMR:
		return r.mmr(ctx, opts)
	case opts.Rerank:
		return r.reranked(ctx, opts)
	case opts.Explain:
		ret, err := r.explain(ctx, opts.Collections, opts.Query, opts.K, opts.Source, opts.Filter, opts.Hybrid)
		if err != nil {
			return nil, nil, err
		}
		return ret.Hits, NextScorePtr(ret), nil
	default:
		hits, err := r.query(ctx, opts.Collections, opts.Query, opts.K, opts.Source, opts.Filter, opts.Hybrid)
		return hits, nil, err
	}
}

// lexical runs BM25-only retrieval (no query embedding), for querying a
// collection with no usable embedder. It is mutually exclusive with the
// vector-based reorderings and is single-collection (cross-collection lexical
// fusion is not implemented).
func (r *Retriever) lexical(ctx context.Context, opts RetrieveOptions) ([]domain.ChunkHit, *float64, error) {
	if opts.Hybrid || opts.Rerank || opts.MMR || opts.Recency {
		return nil, nil, fmt.Errorf("%w: --lexical is a vector-free mode and cannot be combined with --hybrid/--rerank/--mmr/--recency", domain.ErrInvalidArgument)
	}
	if len(opts.Collections) != 1 {
		return nil, nil, fmt.Errorf("%w: --lexical is single-collection; query each collection separately", domain.ErrInvalidArgument)
	}
	if opts.Explain {
		ret, err := r.querier.ExplainLexical(ctx, opts.Collections[0], opts.Query, opts.K, opts.Source, opts.Filter)
		if err != nil {
			return nil, nil, err
		}
		return ret.Hits, NextScorePtr(ret), nil
	}
	hits, err := r.querier.QueryLexical(ctx, opts.Collections[0], opts.Query, opts.K, opts.Source, opts.Filter)
	return hits, nil, err
}

// reranked fetches a wide candidate pool and reorders it to the top-k with the
// cross-encoder. With Explain it reranks the whole pool so the runner-up (the
// k+1th by rerank score) is visible.
func (r *Retriever) reranked(ctx context.Context, opts RetrieveOptions) ([]domain.ChunkHit, *float64, error) {
	if r.rerank == nil {
		return nil, nil, fmt.Errorf("%w: rerank requested but no rerank provider is configured", domain.ErrInvalidArgument)
	}
	if opts.Candidates < opts.K {
		return nil, nil, fmt.Errorf("%w: rerank candidate pool (%d) must be >= k (%d)", domain.ErrInvalidArgument, opts.Candidates, opts.K)
	}
	pool, err := r.query(ctx, opts.Collections, opts.Query, opts.Candidates, opts.Source, opts.Filter, opts.Hybrid)
	if err != nil {
		return nil, nil, err
	}
	topN := opts.K
	if opts.Explain {
		topN = 0
	}
	reranked, err := r.rerank.Rerank(ctx, opts.Query, pool, topN)
	if err != nil {
		return nil, nil, err
	}
	var runnerUp *float64
	if opts.Explain && opts.K > 0 && len(reranked) > opts.K {
		if rs := reranked[opts.K].RerankScore; rs != nil {
			s := *rs
			runnerUp = &s
		}
		reranked = reranked[:opts.K]
	}
	return reranked, runnerUp, nil
}

// mmr selects the final top-k from a wider pool by Maximal Marginal Relevance.
// It is single-collection (the redundancy penalty needs the pool's own vectors,
// read from VectorIndex.Entries).
func (r *Retriever) mmr(ctx context.Context, opts RetrieveOptions) ([]domain.ChunkHit, *float64, error) {
	if opts.Rerank {
		return nil, nil, fmt.Errorf("%w: MMR and rerank are mutually exclusive (both reorder the candidate pool)", domain.ErrInvalidArgument)
	}
	if len(opts.Collections) > 1 {
		return nil, nil, fmt.Errorf("%w: MMR does not support multiple collections; query each separately", domain.ErrInvalidArgument)
	}
	if r.index == nil {
		return nil, nil, fmt.Errorf("%w: MMR needs the vector index, which is not available", domain.ErrInvalidArgument)
	}
	pool := opts.K
	if pool < poolMin {
		pool = poolMin
	}
	hits, err := r.query(ctx, opts.Collections, opts.Query, pool, opts.Source, opts.Filter, opts.Hybrid)
	if err != nil {
		return nil, nil, err
	}
	entries, err := r.index.Entries(ctx, opts.Collections[0])
	if err != nil {
		return nil, nil, fmt.Errorf("read vectors for MMR: %w", err)
	}
	vecByID := make(map[domain.ChunkID][]float32, len(entries))
	for _, e := range entries {
		vecByID[e.ChunkID] = e.Vector
	}
	cands := make([]domain.MMRCandidate, len(hits))
	for i, h := range hits {
		cands[i] = domain.MMRCandidate{Hit: h, Vector: vecByID[h.Chunk.ID]}
	}
	return domain.SelectMMR(cands, opts.MMRLambda, opts.K), nil, nil
}

// recency reorders a wider pool by relevance blended with a time decay, then
// trims to top-k. It needs no vectors, so it composes with hybrid, source,
// filter, and multiple collections.
func (r *Retriever) recency(ctx context.Context, opts RetrieveOptions) ([]domain.ChunkHit, *float64, error) {
	if opts.Rerank {
		return nil, nil, fmt.Errorf("%w: recency and rerank are mutually exclusive (both reorder the candidate pool)", domain.ErrInvalidArgument)
	}
	if opts.HalfLife <= 0 {
		return nil, nil, fmt.Errorf("%w: recency half-life must be > 0", domain.ErrInvalidArgument)
	}
	pool := opts.K
	if pool < poolMin {
		pool = poolMin
	}
	hits, err := r.query(ctx, opts.Collections, opts.Query, pool, opts.Source, opts.Filter, opts.Hybrid)
	if err != nil {
		return nil, nil, err
	}
	return domain.DecayByRecency(hits, opts.HalfLife, r.now(), opts.K), nil, nil
}

// query retrieves the top-k hits for one collection, or merges across several
// same-space collections; hybrid fuses vector and BM25 results.
func (r *Retriever) query(ctx context.Context, collections []string, query string, k int, source string, filter domain.Predicate, hybrid bool) ([]domain.ChunkHit, error) {
	if len(collections) > 1 {
		if hybrid {
			return nil, errHybridMultiCollection()
		}
		return r.querier.QueryAcross(ctx, collections, query, k, source, filter)
	}
	if hybrid {
		return r.querier.QueryHybrid(ctx, collections[0], query, k, source, filter)
	}
	return r.querier.Query(ctx, collections[0], query, k, source, filter)
}

// explain is query's diagnostic twin: it also surfaces the runner-up just outside
// the returned top-k.
func (r *Retriever) explain(ctx context.Context, collections []string, query string, k int, source string, filter domain.Predicate, hybrid bool) (Retrieval, error) {
	if len(collections) > 1 {
		if hybrid {
			return Retrieval{}, errHybridMultiCollection()
		}
		return r.querier.ExplainAcross(ctx, collections, query, k, source, filter)
	}
	if hybrid {
		return r.querier.ExplainHybrid(ctx, collections[0], query, k, source, filter)
	}
	return r.querier.Explain(ctx, collections[0], query, k, source, filter)
}

// errHybridMultiCollection is the usage error for combining hybrid retrieval with
// multiple collections, whose lexical indexes cannot be fused across spaces yet.
func errHybridMultiCollection() error {
	return fmt.Errorf("%w: hybrid retrieval does not support multiple collections; query each separately", domain.ErrInvalidArgument)
}

// NextScorePtr returns a retrieval's runner-up score as a pointer, or nil when
// there was no further candidate (so --json renders next_score: null).
func NextScorePtr(ret Retrieval) *float64 {
	if !ret.HasNext {
		return nil
	}
	s := ret.NextScore
	return &s
}
