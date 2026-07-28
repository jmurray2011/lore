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

// testChunkerSpec is the spec the test chunker registry (chunker41) corresponds
// to. Collections pinned to it accept ingestion through that registry.
func testChunkerSpec() domain.ChunkerSpec {
	return domain.ChunkerSpec{Strategy: "fixed", Version: domain.FixedChunkerVersion, Size: 4, Overlap: 1, Tokenizer: "words"}
}

func mustCollection(t *testing.T, name string, space domain.EmbeddingSpace) *domain.Collection {
	t.Helper()
	c, err := domain.NewCollection(name, space, testChunkerSpec(), time.Unix(0, 0).UTC())
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

	newQuerier := func(idx *fakeIndex, docs *fakeDocs, emb *fakeEmbedder, lex app.LexicalIndex) *app.Querier {
		coll := mustCollection(t, "docs", space)
		return app.NewQuerier(newFakeCollections(coll), idx, docs, emb, lex)
	}

	t.Run("returns hydrated hits in score order", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {{ChunkID: c1.ID, Score: 0.9}, {ChunkID: c0.ID, Score: 0.4}},
		}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"hello": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb, &fakeLexical{})

		hits, err := q.Query(ctx, "docs", "hello", 2, "", domain.Predicate{})
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

	t.Run("hydrates source provenance onto hits", func(t *testing.T) {
		doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("NewDocument: %v", err)
		}
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {{ChunkID: c1.ID, Score: 0.9}, {ChunkID: c0.ID, Score: 0.4}},
		}}
		docs := &fakeDocs{
			docs:   map[domain.DocumentID]domain.Document{doc.ID: *doc},
			chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1},
		}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb, &fakeLexical{})

		hits, err := q.Query(ctx, "docs", "q", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(hits) != 2 {
			t.Fatalf("want 2 hits, got %d", len(hits))
		}
		for _, h := range hits {
			if h.Source != "file:///a.md" {
				t.Errorf("hit %s: Source = %q, want file:///a.md", h.Chunk.ID, h.Source)
			}
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
		q := newQuerier(idx, docs, emb, &fakeLexical{})

		hits, err := q.Query(ctx, "docs", "q", 3, "", domain.Predicate{})
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

	t.Run("source filter keeps only matching hits, over-fetching to fill k", func(t *testing.T) {
		da := domain.DeriveDocumentID("docs", "file:///a.md")
		db := domain.DeriveDocumentID("docs", "file:///b.pdf")
		ca := mustChunk(t, da, 0, "alpha")
		cb := mustChunk(t, db, 0, "beta")
		docA, _ := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("a")), time.Unix(0, 0).UTC())
		docB, _ := domain.NewDocument("docs", "file:///b.pdf", domain.HashContent([]byte("b")), time.Unix(0, 0).UTC())
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			// a.md ranks higher, so an unfiltered k=1 would return it.
			"docs": {{ChunkID: ca.ID, Score: 0.9}, {ChunkID: cb.ID, Score: 0.5}},
		}}
		docs := &fakeDocs{
			docs:   map[domain.DocumentID]domain.Document{docA.ID: *docA, docB.ID: *docB},
			chunks: map[domain.ChunkID]domain.Chunk{ca.ID: ca, cb.ID: cb},
		}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb, &fakeLexical{})

		hits, err := q.Query(ctx, "docs", "q", 1, "*.pdf", domain.Predicate{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(hits) != 1 || hits[0].Chunk.ID != cb.ID {
			t.Errorf("want only the .pdf hit, got %+v", hits)
		}
		if idx.gotK <= 1 {
			t.Errorf("expected over-fetch (k > 1) when filtering, got gotK=%d", idx.gotK)
		}
	})

	t.Run("no matches yields no hits, no error", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, &fakeDocs{}, emb, &fakeLexical{})

		hits, err := q.Query(ctx, "docs", "q", 5, "", domain.Predicate{})
		if err != nil || len(hits) != 0 {
			t.Errorf("want no hits, nil; got %v, %v", hits, err)
		}
	})

	t.Run("empty query is ErrInvalidArgument", func(t *testing.T) {
		q := newQuerier(&fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: space}, &fakeLexical{})
		if _, err := q.Query(ctx, "docs", "   ", 5, "", domain.Predicate{}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		q := app.NewQuerier(newFakeCollections(), &fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: space}, &fakeLexical{})
		if _, err := q.Query(ctx, "missing", "q", 5, "", domain.Predicate{}); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("embedder space mismatch is ErrSpaceMismatch", func(t *testing.T) {
		emb := &fakeEmbedder{
			space:  domain.EmbeddingSpace{Model: "other", Dimensions: 5},
			byText: map[string][]float32{"q": {1, 0, 0, 0, 0}},
		}
		q := newQuerier(&fakeIndex{}, &fakeDocs{}, emb, &fakeLexical{})
		if _, err := q.Query(ctx, "docs", "q", 5, "", domain.Predicate{}); !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Errorf("want ErrSpaceMismatch, got %v", err)
		}
	})

	c2 := mustChunk(t, docID, 2, "gamma text")

	t.Run("Explain surfaces the runner-up (k+1th) score and fetches one extra", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {{ChunkID: c1.ID, Score: 0.9}, {ChunkID: c0.ID, Score: 0.5}, {ChunkID: c2.ID, Score: 0.3}},
		}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1, c2.ID: c2}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb, &fakeLexical{})

		ret, err := q.Explain(ctx, "docs", "q", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		if len(ret.Hits) != 2 || ret.Hits[0].Chunk.ID != c1.ID || ret.Hits[1].Chunk.ID != c0.ID {
			t.Fatalf("returned hits wrong: %+v", ret.Hits)
		}
		if !ret.HasNext || ret.NextScore != 0.3 {
			t.Errorf("want runner-up 0.3, got HasNext=%v NextScore=%v", ret.HasNext, ret.NextScore)
		}
		if idx.gotK != 3 {
			t.Errorf("Explain should fetch k+1=3, searched k=%d", idx.gotK)
		}
	})

	t.Run("Explain reports no runner-up when there is no further candidate", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"docs": {{ChunkID: c1.ID, Score: 0.9}, {ChunkID: c0.ID, Score: 0.5}},
		}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		q := newQuerier(idx, docs, emb, &fakeLexical{})

		ret, err := q.Explain(ctx, "docs", "q", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		if len(ret.Hits) != 2 {
			t.Fatalf("want 2 hits, got %d", len(ret.Hits))
		}
		if ret.HasNext {
			t.Errorf("want no runner-up, got NextScore=%v", ret.NextScore)
		}
	})
}

func TestQuerierHybrid(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	cv := mustChunk(t, docID, 0, "vector hit")  // returned by the vector index
	cl := mustChunk(t, docID, 1, "lexical hit") // returned only by the lexical index
	coll := mustCollection(t, "docs", space)

	idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
		"docs": {{ChunkID: cv.ID, Score: 0.8}},
	}}
	lex := &fakeLexical{results: map[string][]domain.ChunkID{
		"docs": {cl.ID, cv.ID}, // cl ranks first lexically; cv also appears
	}}
	docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{cv.ID: cv, cl.ID: cl}}
	emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
	q := app.NewQuerier(newFakeCollections(coll), idx, docs, emb, lex)

	hits, err := q.QueryHybrid(ctx, "docs", "q", 10, "", domain.Predicate{})
	if err != nil {
		t.Fatalf("QueryHybrid: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 fused hits, got %d: %+v", len(hits), hits)
	}
	// cv is in both lists, so RRF ranks it above the lexical-only cl.
	if hits[0].Chunk.ID != cv.ID {
		t.Errorf("a chunk in both lists should rank first, got %s", hits[0].Chunk.ID)
	}
	byID := map[domain.ChunkID]domain.ChunkHit{}
	for _, h := range hits {
		byID[h.Chunk.ID] = h
	}
	if byID[cv.ID].Score != 0.8 {
		t.Errorf("a vector-found hit must keep its cosine score, got %v", byID[cv.ID].Score)
	}
	if byID[cl.ID].Score != 0 {
		t.Errorf("a lexical-only hit has no cosine, want 0, got %v", byID[cl.ID].Score)
	}

	// The --where predicate must reach the lexical index too, not just the vector one.
	where, _ := domain.ParseWhere([]string{"author=alice"})
	if _, err := q.QueryHybrid(ctx, "docs", "q", 10, "", where); err != nil {
		t.Fatalf("QueryHybrid with filter: %v", err)
	}
	if lex.gotFilter.IsZero() {
		t.Error("the --where predicate was not passed to the lexical index")
	}
}

func TestQuerierQueryFrom(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	// Source collection v1: two chunks whose vectors are already stored.
	v1DocID := domain.DeriveDocumentID("v1", "file:///v1.md")
	sc0 := mustChunk(t, v1DocID, 0, "source chunk zero")
	sc1 := mustChunk(t, v1DocID, 1, "source chunk one")
	v1Doc, _ := domain.NewDocument("v1", "file:///v1.md", domain.HashContent([]byte("v1")), time.Unix(0, 0).UTC())

	// Target collection v2: two chunks the search returns.
	v2DocID := domain.DeriveDocumentID("v2", "file:///v2.md")
	tc0 := mustChunk(t, v2DocID, 0, "target chunk zero")
	tc1 := mustChunk(t, v2DocID, 1, "target chunk one")
	v2Doc, _ := domain.NewDocument("v2", "file:///v2.md", domain.HashContent([]byte("v2")), time.Unix(0, 0).UTC())

	newFrom := func(idx *fakeIndex, docs *fakeDocs, emb *fakeEmbedder, sourceSpace domain.EmbeddingSpace) *app.Querier {
		v2 := mustCollection(t, "v2", space)
		v1 := mustCollection(t, "v1", sourceSpace)
		return app.NewQuerier(newFakeCollections(v1, v2), idx, docs, emb, &fakeLexical{})
	}

	setup := func() (*fakeIndex, *fakeDocs, *fakeEmbedder) {
		idx := &fakeIndex{
			upserted: map[string]map[domain.ChunkID][]float32{
				"v1": {sc0.ID: {1, 0, 0}, sc1.ID: {0, 1, 0}},
			},
			matches: map[string][]domain.VectorMatch{
				"v2": {{ChunkID: tc0.ID, Score: 0.9}, {ChunkID: tc1.ID, Score: 0.5}},
			},
		}
		docs := &fakeDocs{
			docs: map[domain.DocumentID]domain.Document{v1Doc.ID: *v1Doc, v2Doc.ID: *v2Doc},
			chunks: map[domain.ChunkID]domain.Chunk{
				sc0.ID: sc0, sc1.ID: sc1, tc0.ID: tc0, tc1.ID: tc1,
			},
		}
		emb := &fakeEmbedder{space: space}
		return idx, docs, emb
	}

	t.Run("groups each source chunk's target hits, without re-embedding", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newFrom(idx, docs, emb, space)

		groups, err := q.QueryFrom(ctx, "v2", "v1", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("QueryFrom: %v", err)
		}
		if len(groups) != 2 {
			t.Fatalf("want 2 groups (one per source chunk), got %d", len(groups))
		}
		// Groups are ordered by source provenance (URI, then seq).
		if groups[0].From.Chunk.ID != sc0.ID || groups[1].From.Chunk.ID != sc1.ID {
			t.Errorf("group order = %s, %s; want %s, %s", groups[0].From.Chunk.ID, groups[1].From.Chunk.ID, sc0.ID, sc1.ID)
		}
		for _, g := range groups {
			if g.From.Source != "file:///v1.md" {
				t.Errorf("from.Source = %q, want file:///v1.md", g.From.Source)
			}
			if len(g.Hits) != 2 || g.Hits[0].Chunk.ID != tc0.ID || g.Hits[1].Chunk.ID != tc1.ID {
				t.Errorf("hits = %+v, want [%s %s]", g.Hits, tc0.ID, tc1.ID)
			}
			if g.Hits[0].Source != "file:///v2.md" {
				t.Errorf("target hit Source = %q, want file:///v2.md", g.Hits[0].Source)
			}
		}
		if n := emb.embedCalls.Load(); n != 0 {
			t.Errorf("QueryFrom must not embed; embedder was called %d times", n)
		}
		if idx.gotCollection != "v2" || idx.gotK != 2 {
			t.Errorf("search target = %q k=%d, want v2 k=2", idx.gotCollection, idx.gotK)
		}
	})

	t.Run("mismatched spaces is ErrSpaceMismatch, with no search and no embed", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newFrom(idx, docs, emb, domain.EmbeddingSpace{Model: "other", Dimensions: 7})

		_, err := q.QueryFrom(ctx, "v2", "v1", 2, "", domain.Predicate{})
		if !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Fatalf("want ErrSpaceMismatch, got %v", err)
		}
		if idx.gotCollection != "" {
			t.Errorf("no search should run on a space mismatch; searched %q", idx.gotCollection)
		}
		if n := emb.embedCalls.Load(); n != 0 {
			t.Errorf("no embed should run; embedder called %d times", n)
		}
	})

	t.Run("unknown source or target collection is ErrNotFound", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newFrom(idx, docs, emb, space)
		if _, err := q.QueryFrom(ctx, "v2", "ghost", 2, "", domain.Predicate{}); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("unknown source: want ErrNotFound, got %v", err)
		}
		if _, err := q.QueryFrom(ctx, "ghost", "v1", 2, "", domain.Predicate{}); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("unknown target: want ErrNotFound, got %v", err)
		}
	})

	t.Run("empty source collection yields no groups", func(t *testing.T) {
		idx, docs, emb := setup()
		idx.upserted["v1"] = map[domain.ChunkID][]float32{} // no stored vectors
		q := newFrom(idx, docs, emb, space)
		groups, err := q.QueryFrom(ctx, "v2", "v1", 2, "", domain.Predicate{})
		if err != nil || len(groups) != 0 {
			t.Errorf("want no groups, nil; got %d groups, %v", len(groups), err)
		}
	})
}

func TestQuerierAcross(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	aDocID := domain.DeriveDocumentID("a", "file:///a.md")
	bDocID := domain.DeriveDocumentID("b", "file:///b.md")
	a0 := mustChunk(t, aDocID, 0, "alpha zero")
	a1 := mustChunk(t, aDocID, 1, "alpha one")
	b0 := mustChunk(t, bDocID, 0, "beta zero")
	b1 := mustChunk(t, bDocID, 1, "beta one")
	aDoc, _ := domain.NewDocument("a", "file:///a.md", domain.HashContent([]byte("a")), time.Unix(0, 0).UTC())
	bDoc, _ := domain.NewDocument("b", "file:///b.md", domain.HashContent([]byte("b")), time.Unix(0, 0).UTC())

	newAcross := func(idx *fakeIndex, docs *fakeDocs, emb *fakeEmbedder, bSpace domain.EmbeddingSpace) *app.Querier {
		a := mustCollection(t, "a", space)
		b := mustCollection(t, "b", bSpace)
		return app.NewQuerier(newFakeCollections(a, b), idx, docs, emb, &fakeLexical{})
	}

	setup := func() (*fakeIndex, *fakeDocs, *fakeEmbedder) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
			"a": {{ChunkID: a0.ID, Score: 0.9}, {ChunkID: a1.ID, Score: 0.3}},
			"b": {{ChunkID: b0.ID, Score: 0.7}, {ChunkID: b1.ID, Score: 0.2}},
		}}
		docs := &fakeDocs{
			docs:   map[domain.DocumentID]domain.Document{aDoc.ID: *aDoc, bDoc.ID: *bDoc},
			chunks: map[domain.ChunkID]domain.Chunk{a0.ID: a0, a1.ID: a1, b0.ID: b0, b1.ID: b1},
		}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"q": {1, 0, 0}}}
		return idx, docs, emb
	}

	t.Run("merges hits across collections by score, tagging each with its origin", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newAcross(idx, docs, emb, space)

		hits, err := q.QueryAcross(ctx, []string{"a", "b"}, "q", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("QueryAcross: %v", err)
		}
		if len(hits) != 2 {
			t.Fatalf("want top-2 merged, got %d", len(hits))
		}
		if hits[0].Chunk.ID != a0.ID || hits[0].Collection != "a" {
			t.Errorf("hit[0] = %+v, want a0 from collection a", hits[0])
		}
		if hits[1].Chunk.ID != b0.ID || hits[1].Collection != "b" {
			t.Errorf("hit[1] = %+v, want b0 from collection b", hits[1])
		}
		if n := emb.embedCalls.Load(); n != 1 {
			t.Errorf("the query should be embedded exactly once, got %d", n)
		}
	})

	t.Run("ExplainAcross surfaces the merged runner-up", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newAcross(idx, docs, emb, space)

		ret, err := q.ExplainAcross(ctx, []string{"a", "b"}, "q", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("ExplainAcross: %v", err)
		}
		if len(ret.Hits) != 2 {
			t.Fatalf("want 2 hits, got %d", len(ret.Hits))
		}
		if !ret.HasNext || ret.NextScore != 0.3 {
			t.Errorf("want merged runner-up 0.3, got HasNext=%v NextScore=%v", ret.HasNext, ret.NextScore)
		}
	})

	t.Run("mismatched spaces is ErrSpaceMismatch, with no embed and no search", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newAcross(idx, docs, emb, domain.EmbeddingSpace{Model: "other", Dimensions: 7})

		_, err := q.QueryAcross(ctx, []string{"a", "b"}, "q", 2, "", domain.Predicate{})
		if !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Fatalf("want ErrSpaceMismatch, got %v", err)
		}
		if n := emb.embedCalls.Load(); n != 0 {
			t.Errorf("no embed on space mismatch, got %d", n)
		}
		if idx.gotCollection != "" {
			t.Errorf("no search on space mismatch, searched %q", idx.gotCollection)
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newAcross(idx, docs, emb, space)
		if _, err := q.QueryAcross(ctx, []string{"a", "ghost"}, "q", 2, "", domain.Predicate{}); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("duplicate collection names are deduplicated", func(t *testing.T) {
		idx, docs, emb := setup()
		q := newAcross(idx, docs, emb, space)
		hits, err := q.QueryAcross(ctx, []string{"a", "a"}, "q", 2, "", domain.Predicate{})
		if err != nil {
			t.Fatalf("QueryAcross: %v", err)
		}
		// Only collection a's two chunks, not doubled.
		if len(hits) != 2 || hits[0].Chunk.ID != a0.ID || hits[1].Chunk.ID != a1.ID {
			t.Errorf("dedup failed: %+v", hits)
		}
	})
}
