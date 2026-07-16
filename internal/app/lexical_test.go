package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestQuerierLexicalNeedsNoEmbedder(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	c0 := mustChunk(t, docID, 0, "alpha text")
	c1 := mustChunk(t, docID, 1, "beta text")

	coll := mustCollection(t, "docs", space)
	docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1}}
	lex := &fakeLexical{results: map[string][]domain.ChunkID{"docs": {c1.ID, c0.ID}}}
	// An embedder that errors on Embed: the lexical path must never call it — that
	// is the whole point (query a collection with no usable embedder / API key).
	emb := &fakeEmbedder{space: space, embedErr: errors.New("provider down")}
	q := app.NewQuerier(newFakeCollections(coll), &fakeIndex{}, docs, emb, lex)

	hits, err := q.QueryLexical(ctx, "docs", "alpha beta", 5, "", domain.Predicate{})
	if err != nil {
		t.Fatalf("QueryLexical: %v", err)
	}
	if len(hits) != 2 || hits[0].Chunk.ID != c1.ID {
		t.Fatalf("want 2 lexical hits in BM25 order (c1 first), got %+v", hits)
	}
	if n := emb.embedCalls.Load(); n != 0 {
		t.Errorf("lexical retrieval must not call the embedder, got %d calls", n)
	}
}

func TestQuerierLexicalNoIndexIsUsageError(t *testing.T) {
	coll := mustCollection(t, "docs", testSpace())
	q := app.NewQuerier(newFakeCollections(coll), &fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: testSpace()}, nil)
	_, err := q.QueryLexical(context.Background(), "docs", "x", 5, "", domain.Predicate{})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument when no lexical index is wired, got %v", err)
	}
}

func TestRetrieveLexicalDispatchAndExclusivity(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	c0 := mustChunk(t, docID, 0, "alpha text")
	docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0}}
	lex := &fakeLexical{results: map[string][]domain.ChunkID{"docs": {c0.ID}}}
	emb := &fakeEmbedder{space: space, embedErr: errors.New("provider down")}
	q := app.NewQuerier(newFakeCollections(mustCollection(t, "docs", space)), &fakeIndex{}, docs, emb, lex)
	r := app.NewRetriever(q, nil, &fakeIndex{})

	hits, _, err := r.Resolve(ctx, app.RetrieveOptions{Collections: []string{"docs"}, Query: "alpha", K: 5, Lexical: true})
	if err != nil {
		t.Fatalf("lexical Resolve: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk.ID != c0.ID {
		t.Fatalf("want the lexical hit, got %+v", hits)
	}

	for _, bad := range []app.RetrieveOptions{
		{Collections: []string{"docs"}, Query: "a", K: 5, Lexical: true, Hybrid: true},
		{Collections: []string{"docs"}, Query: "a", K: 5, Lexical: true, Rerank: true},
		{Collections: []string{"docs"}, Query: "a", K: 5, Lexical: true, MMR: true},
		{Collections: []string{"docs", "other"}, Query: "a", K: 5, Lexical: true},
	} {
		if _, _, err := r.Resolve(ctx, bad); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("opts %+v: want ErrInvalidArgument, got %v", bad, err)
		}
	}
}
