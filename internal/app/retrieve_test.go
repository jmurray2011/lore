package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestRetriever(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	c0 := mustChunk(t, docID, 0, "alpha text")
	c1 := mustChunk(t, docID, 1, "beta text")

	build := func() *app.Retriever {
		coll := mustCollection(t, "docs", space)
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {{ChunkID: c0.ID, Score: 0.9}, {ChunkID: c1.ID, Score: 0.5}},
		}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := app.NewQuerier(newFakeCollections(coll), idx, docs, emb, &fakeLexical{})
		prov := &fakeRerankProvider{results: []app.RankResult{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.5}}}
		return app.NewRetriever(q, app.NewReranker(prov), idx)
	}

	t.Run("plain resolve returns hydrated hits, no runner-up", func(t *testing.T) {
		hits, runnerUp, err := build().Resolve(ctx, app.RetrieveOptions{Collections: []string{"docs"}, Query: "q", K: 2})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(hits) != 2 || runnerUp != nil {
			t.Fatalf("hits=%d runnerUp=%v, want 2 hits and nil runner-up", len(hits), runnerUp)
		}
	})

	t.Run("max-per-source caps inside Resolve", func(t *testing.T) {
		hits, _, err := build().Resolve(ctx, app.RetrieveOptions{Collections: []string{"docs"}, Query: "q", K: 2, MaxPerSource: 1})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(hits) != 1 {
			t.Fatalf("want 1 hit after the per-source cap, got %d", len(hits))
		}
	})

	t.Run("rerank reorders via the provider", func(t *testing.T) {
		hits, _, err := build().Resolve(ctx, app.RetrieveOptions{Collections: []string{"docs"}, Query: "q", K: 2, Candidates: 50, Rerank: true})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(hits) == 0 || hits[0].Chunk.ID != c1.ID {
			t.Fatalf("rerank should put provider index 1 (c1) first, got %+v", hits)
		}
	})

	t.Run("mutually-exclusive and misconfigured options are usage errors", func(t *testing.T) {
		base := app.RetrieveOptions{Collections: []string{"docs"}, Query: "q", K: 2, Candidates: 50, MMRLambda: 0.5, HalfLife: time.Hour}
		cases := []struct {
			name string
			mut  func(*app.RetrieveOptions)
		}{
			{"recency+mmr", func(o *app.RetrieveOptions) { o.Recency, o.MMR = true, true }},
			{"mmr+rerank", func(o *app.RetrieveOptions) { o.MMR, o.Rerank = true, true }},
			{"recency+rerank", func(o *app.RetrieveOptions) { o.Recency, o.Rerank = true, true }},
			{"mmr multi-collection", func(o *app.RetrieveOptions) { o.MMR = true; o.Collections = []string{"docs", "other"} }},
			{"hybrid multi-collection", func(o *app.RetrieveOptions) { o.Hybrid = true; o.Collections = []string{"docs", "other"} }},
			{"rerank candidates < k", func(o *app.RetrieveOptions) { o.Rerank = true; o.Candidates = 1 }},
		}
		for _, tc := range cases {
			opts := base
			tc.mut(&opts)
			if _, _, err := build().Resolve(ctx, opts); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("%s: want ErrInvalidArgument, got %v", tc.name, err)
			}
		}
	})

	t.Run("rerank without a provider is a usage error", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		idx := &fakeIndex{}
		q := app.NewQuerier(newFakeCollections(coll), idx, &fakeDocs{}, &fakeEmbedder{space: space}, &fakeLexical{})
		r := app.NewRetriever(q, nil, idx)
		_, _, err := r.Resolve(ctx, app.RetrieveOptions{Collections: []string{"docs"}, Query: "q", K: 2, Candidates: 50, Rerank: true})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})
}
