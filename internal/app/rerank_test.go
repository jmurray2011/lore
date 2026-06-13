package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

type fakeRerankProvider struct {
	results  []app.RankResult
	err      error
	called   bool
	gotQuery string
	gotDocs  []string
	gotTopN  int
}

func (f *fakeRerankProvider) Rerank(_ context.Context, query string, documents []string, topN int) ([]app.RankResult, error) {
	f.called = true
	f.gotQuery, f.gotDocs, f.gotTopN = query, documents, topN
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func TestReranker(t *testing.T) {
	ctx := context.Background()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")

	hit := func(seq int, text string, score float64) domain.ChunkHit {
		c := mustChunk(t, docID, seq, text)
		return domain.ChunkHit{Chunk: c, Score: score, Source: "file:///a.md"}
	}
	hits := []domain.ChunkHit{
		hit(0, "alpha", 0.30),
		hit(1, "beta", 0.80), // highest by similarity...
		hit(2, "gamma", 0.20),
	}

	t.Run("reorders by rerank score, attaches it, preserves the similarity score", func(t *testing.T) {
		// Provider reranks: gamma (idx 2) best, then alpha (idx 0), then beta (idx 1).
		prov := &fakeRerankProvider{results: []app.RankResult{{Index: 2, Score: 0.95}, {Index: 0, Score: 0.60}, {Index: 1, Score: 0.10}}}
		r := app.NewReranker(prov)

		got, err := r.Rerank(ctx, "q", hits, 0)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("want 3 hits, got %d", len(got))
		}
		// Reordered by rerank score (gamma, alpha, beta), not similarity.
		if got[0].Chunk.Seq != 2 || got[1].Chunk.Seq != 0 || got[2].Chunk.Seq != 1 {
			t.Errorf("rerank order wrong: %d,%d,%d", got[0].Chunk.Seq, got[1].Chunk.Seq, got[2].Chunk.Seq)
		}
		if got[0].RerankScore == nil || *got[0].RerankScore != 0.95 {
			t.Errorf("rerank score not attached: %+v", got[0].RerankScore)
		}
		// Original similarity score preserved (gamma's was 0.20).
		if got[0].Score != 0.20 {
			t.Errorf("similarity score lost: got %v, want 0.20", got[0].Score)
		}
		if prov.gotQuery != "q" || len(prov.gotDocs) != 3 || prov.gotDocs[1] != "beta" {
			t.Errorf("provider got wrong inputs: q=%q docs=%v", prov.gotQuery, prov.gotDocs)
		}
	})

	t.Run("topN truncates after reranking", func(t *testing.T) {
		prov := &fakeRerankProvider{results: []app.RankResult{{Index: 2, Score: 0.95}, {Index: 0, Score: 0.60}, {Index: 1, Score: 0.10}}}
		r := app.NewReranker(prov)
		got, err := r.Rerank(ctx, "q", hits, 2)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(got) != 2 || got[0].Chunk.Seq != 2 || got[1].Chunk.Seq != 0 {
			t.Errorf("topN=2 should keep the top two reranked: %+v", got)
		}
	})

	t.Run("topN<=0 re-emits all hits, reordered", func(t *testing.T) {
		prov := &fakeRerankProvider{results: []app.RankResult{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.5}, {Index: 2, Score: 0.4}}}
		r := app.NewReranker(prov)
		got, err := r.Rerank(ctx, "q", hits, 0)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("topN omitted must re-emit all, got %d", len(got))
		}
	})

	t.Run("empty input returns empty without calling the provider", func(t *testing.T) {
		prov := &fakeRerankProvider{}
		r := app.NewReranker(prov)
		got, err := r.Rerank(ctx, "q", nil, 5)
		if err != nil || len(got) != 0 {
			t.Errorf("want empty/nil, got %v / %v", got, err)
		}
		if prov.called {
			t.Error("provider must not be called for empty input")
		}
	})

	t.Run("provider error propagates", func(t *testing.T) {
		prov := &fakeRerankProvider{err: errors.New("rerank down")}
		r := app.NewReranker(prov)
		if _, err := r.Rerank(ctx, "q", hits, 0); err == nil {
			t.Error("want the provider error to propagate")
		}
	})

	t.Run("out-of-range index is an error", func(t *testing.T) {
		prov := &fakeRerankProvider{results: []app.RankResult{{Index: 9, Score: 0.5}}}
		r := app.NewReranker(prov)
		if _, err := r.Rerank(ctx, "q", hits, 0); err == nil {
			t.Error("want an error for an out-of-range result index")
		}
	})
}
