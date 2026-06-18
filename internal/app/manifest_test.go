package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func seedDoc(t *testing.T, docs *fakeDocs, collection, uri string) *domain.Document {
	t.Helper()
	d, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(uri)), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCorpusRefsOf_DigestMatchesSnapshot(t *testing.T) {
	t.Parallel()
	space := testSpace()
	docs := &fakeDocs{}
	a := seedDoc(t, docs, "docs", "file:///a.md")
	b := seedDoc(t, docs, "docs", "file:///b.md")
	cat := app.NewCatalog(newFakeCollections(mustCollection(t, "docs", space)), docs, &fakeEmbedder{space: space}, chunker41(t))

	refs, err := app.CorpusRefsOf(context.Background(), cat, []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(refs))
	}
	got := refs[0]
	if got.Collection != "docs" {
		t.Errorf("collection = %q, want docs", got.Collection)
	}
	want := domain.SnapshotOf([]*domain.Document{a, b}).Digest
	if got.Digest != want {
		t.Errorf("digest = %q, want %q (SnapshotOf the seeded docs)", got.Digest, want)
	}
	if got.Model != space.Model || got.Dimensions != space.Dimensions {
		t.Errorf("space = (%q,%d), want (%q,%d)", got.Model, got.Dimensions, space.Model, space.Dimensions)
	}
}

func TestCorpusRefsOf_PreservesQueryOrder(t *testing.T) {
	t.Parallel()
	space := testSpace()
	docs := &fakeDocs{}
	seedDoc(t, docs, "alpha", "file:///a.md")
	seedDoc(t, docs, "beta", "file:///b.md")
	cat := app.NewCatalog(
		newFakeCollections(mustCollection(t, "alpha", space), mustCollection(t, "beta", space)),
		docs, &fakeEmbedder{space: space}, chunker41(t),
	)

	// Refs follow the caller's collection order (primary first), not sorted —
	// the manifest records what was asked.
	refs, err := app.CorpusRefsOf(context.Background(), cat, []string{"beta", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Collection != "beta" || refs[1].Collection != "alpha" {
		t.Fatalf("refs = %+v, want [beta, alpha] in query order", refs)
	}
}

func TestCorpusRefsOf_UnknownCollectionErrors(t *testing.T) {
	t.Parallel()
	space := testSpace()
	cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: space}, chunker41(t))
	if _, err := app.CorpusRefsOf(context.Background(), cat, []string{"ghost"}); err == nil {
		t.Fatal("want error for unknown collection, got nil")
	}
}
