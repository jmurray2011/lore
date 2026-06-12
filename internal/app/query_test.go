package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func testSpace() domain.EmbeddingSpace {
	return domain.EmbeddingSpace{Model: "test-embed", Dimensions: 3}
}

func mustCollection(t *testing.T, name string, space domain.EmbeddingSpace) *domain.Collection {
	t.Helper()
	c, err := domain.NewCollection(name, space, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("NewCollection: %v", err)
	}
	return c
}

func mustChunk(t *testing.T, docID domain.DocumentID, seq int, text string) domain.Chunk {
	t.Helper()
	c, err := domain.NewChunk(docID, seq, text)
	if err != nil {
		t.Fatalf("NewChunk: %v", err)
	}
	return c
}

func TestQuerier(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	c0 := mustChunk(t, docID, 0, "alpha text")
	c1 := mustChunk(t, docID, 1, "beta text")

	newQuerier := func(idx *fakeIndex, docs *fakeDocs, emb *fakeEmbedder) *app.Querier {
		coll := mustCollection(t, "docs", space)
		return app.NewQuerier(newFakeCollections(coll), idx, docs, emb)
	}

	t.Run("returns hydrated hits in score order", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {{ChunkID: c1.ID, Score: 0.9}, {ChunkID: c0.ID, Score: 0.4}},
		}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"hello": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb)

		hits, err := q.Query(ctx, "docs", "hello", 2)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(hits) != 2 {
			t.Fatalf("want 2 hits, got %d", len(hits))
		}
		if hits[0].Chunk.ID != c1.ID || hits[0].Score != 0.9 {
			t.Errorf("hit[0] = %+v, want chunk %s score 0.9", hits[0], c1.ID)
		}
		if hits[1].Chunk.ID != c0.ID || hits[1].Score != 0.4 {
			t.Errorf("hit[1] = %+v, want chunk %s score 0.4", hits[1], c0.ID)
		}
		if idx.gotCollection != "docs" || idx.gotK != 2 {
			t.Errorf("search got collection %q k %d", idx.gotCollection, idx.gotK)
		}
		if !slices.Equal(idx.gotQuery, []float32{1, 0, 0}) {
			t.Errorf("search got query vector %v", idx.gotQuery)
		}
	})

	t.Run("skips matches with no stored chunk, keeps scores aligned", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {
				{ChunkID: c1.ID, Score: 0.9},
				{ChunkID: "ghost", Score: 0.7},
				{ChunkID: c0.ID, Score: 0.4},
			},
		}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb)

		hits, err := q.Query(ctx, "docs", "q", 3)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(hits) != 2 {
			t.Fatalf("want 2 hits (ghost skipped), got %d", len(hits))
		}
		if hits[0].Chunk.ID != c1.ID || hits[1].Chunk.ID != c0.ID {
			t.Errorf("order/skip wrong: %s then %s", hits[0].Chunk.ID, hits[1].Chunk.ID)
		}
		if hits[1].Score != 0.4 {
			t.Errorf("score misaligned after skip: %v", hits[1].Score)
		}
	})

	t.Run("no matches yields no hits, no error", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, &fakeDocs{}, emb)

		hits, err := q.Query(ctx, "docs", "q", 5)
		if err != nil || len(hits) != 0 {
			t.Errorf("want no hits, nil; got %v, %v", hits, err)
		}
	})

	t.Run("empty query is ErrInvalidArgument", func(t *testing.T) {
		q := newQuerier(&fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: space})
		if _, err := q.Query(ctx, "docs", "   ", 5); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		q := app.NewQuerier(newFakeCollections(), &fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: space})
		if _, err := q.Query(ctx, "missing", "q", 5); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("embedder space mismatch is ErrSpaceMismatch", func(t *testing.T) {
		emb := &fakeEmbedder{
			space:  domain.EmbeddingSpace{Model: "other", Dimensions: 5},
			byText: map[string][]float32{"q": {1, 0, 0, 0, 0}},
		}
		q := newQuerier(&fakeIndex{}, &fakeDocs{}, emb)
		if _, err := q.Query(ctx, "docs", "q", 5); !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Errorf("want ErrSpaceMismatch, got %v", err)
		}
	})
}
