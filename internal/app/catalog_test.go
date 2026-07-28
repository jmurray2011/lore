package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestCatalogInit(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	t.Run("creates a collection pinned to the embedder space", func(t *testing.T) {
		colls := newFakeCollections()
		cat := app.NewCatalog(colls, &fakeDocs{}, &fakeEmbedder{space: space}, chunker41(t))

		coll, err := cat.Init(ctx, "docs")
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		if coll.Name != "docs" || !coll.Space.Equal(space) {
			t.Errorf("collection = %+v", coll)
		}
		if got, err := colls.Get(ctx, "docs"); err != nil || got.Name != "docs" {
			t.Errorf("not persisted: got %+v, %v", got, err)
		}
	})

	t.Run("invalid name is ErrInvalidArgument", func(t *testing.T) {
		cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: space}, chunker41(t))
		if _, err := cat.Init(ctx, "Bad Name"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("duplicate is ErrAlreadyExists", func(t *testing.T) {
		colls := newFakeCollections()
		cat := app.NewCatalog(colls, &fakeDocs{}, &fakeEmbedder{space: space}, chunker41(t))
		if _, err := cat.Init(ctx, "docs"); err != nil {
			t.Fatal(err)
		}
		if _, err := cat.Init(ctx, "docs"); !errors.Is(err, app.ErrAlreadyExists) {
			t.Errorf("want ErrAlreadyExists, got %v", err)
		}
	})
}

func TestCatalogListAndGet(t *testing.T) {
	ctx := context.Background()
	cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: testSpace()}, chunker41(t))
	if _, err := cat.Init(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Init(ctx, "beta"); err != nil {
		t.Fatal(err)
	}

	list, err := cat.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List: %d collections, err %v", len(list), err)
	}
	if _, err := cat.Get(ctx, "alpha"); err != nil {
		t.Errorf("Get alpha: %v", err)
	}
	if _, err := cat.Get(ctx, "missing"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("Get missing: want ErrNotFound, got %v", err)
	}
}

func TestCatalogListDocuments(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	seed := func(t *testing.T, docs *fakeDocs, collection, uri string) {
		t.Helper()
		doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(uri)), time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("NewDocument: %v", err)
		}
		if err := docs.Upsert(ctx, doc, nil); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	t.Run("lists only the named collection's documents", func(t *testing.T) {
		docs := &fakeDocs{}
		seed(t, docs, "docs", "file:///a.md")
		seed(t, docs, "docs", "file:///b.md")
		seed(t, docs, "notes", "file:///c.md")
		cat := app.NewCatalog(newFakeCollections(mustCollection(t, "docs", space)), docs, &fakeEmbedder{space: space}, chunker41(t))

		got, err := cat.ListDocuments(ctx, "docs")
		if err != nil {
			t.Fatalf("ListDocuments: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("want 2 documents, got %d", len(got))
		}
		for _, d := range got {
			if d.Collection != "docs" {
				t.Errorf("leaked document from %q", d.Collection)
			}
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: space}, chunker41(t))
		if _, err := cat.ListDocuments(ctx, "missing"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})
}

func TestCatalogDiff(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	seed := func(t *testing.T, docs *fakeDocs, collection, uri, content string) {
		t.Helper()
		doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(content)), time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("NewDocument: %v", err)
		}
		if err := docs.Upsert(ctx, doc, nil); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	docs := &fakeDocs{}
	// old: a.md, shared.md (v1), gone.md
	seed(t, docs, "old", "file:///a.md", "a1")
	seed(t, docs, "old", "file:///shared.md", "v1")
	seed(t, docs, "old", "file:///gone.md", "g")
	// new: a.md (unchanged), shared.md (v2), added.md
	seed(t, docs, "new", "file:///a.md", "a1")
	seed(t, docs, "new", "file:///shared.md", "v2")
	seed(t, docs, "new", "file:///added.md", "x")
	cat := app.NewCatalog(
		newFakeCollections(mustCollection(t, "old", space), mustCollection(t, "new", space)),
		docs, &fakeEmbedder{space: space}, chunker41(t))

	t.Run("classifies added, removed, and changed by source URI and hash", func(t *testing.T) {
		diff, err := cat.Diff(ctx, "old", "new")
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		if len(diff.Added) != 1 || diff.Added[0].SourceURI != "file:///added.md" {
			t.Errorf("Added = %+v, want [added.md]", diff.Added)
		}
		if len(diff.Removed) != 1 || diff.Removed[0].SourceURI != "file:///gone.md" {
			t.Errorf("Removed = %+v, want [gone.md]", diff.Removed)
		}
		if len(diff.Changed) != 1 || diff.Changed[0].SourceURI != "file:///shared.md" {
			t.Fatalf("Changed = %+v, want [shared.md]", diff.Changed)
		}
		ch := diff.Changed[0]
		if ch.From != string(domain.HashContent([]byte("v1"))) || ch.To != string(domain.HashContent([]byte("v2"))) {
			t.Errorf("Changed hashes = %+v, want v1->v2", ch)
		}
	})

	t.Run("an unchanged document appears in no bucket", func(t *testing.T) {
		diff, err := cat.Diff(ctx, "old", "new")
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		for _, r := range append(append([]app.DocRef{}, diff.Added...), diff.Removed...) {
			if r.SourceURI == "file:///a.md" {
				t.Errorf("unchanged a.md should not appear: %+v", diff)
			}
		}
		for _, ch := range diff.Changed {
			if ch.SourceURI == "file:///a.md" {
				t.Errorf("unchanged a.md should not appear as changed: %+v", diff)
			}
		}
	})

	t.Run("a missing collection is ErrNotFound", func(t *testing.T) {
		if _, err := cat.Diff(ctx, "old", "ghost"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
		if _, err := cat.Diff(ctx, "ghost", "new"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})
}

func TestCatalogDocumentChunks(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("a")), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	chunks := []domain.Chunk{mustChunk(t, docID, 0, "first"), mustChunk(t, docID, 1, "second")}
	docs := &fakeDocs{}
	if err := docs.Upsert(ctx, doc, chunks); err != nil {
		t.Fatal(err)
	}
	cat := app.NewCatalog(newFakeCollections(mustCollection(t, "docs", space)), docs, &fakeEmbedder{space: space}, chunker41(t))

	t.Run("returns the document's chunks in seq order", func(t *testing.T) {
		got, err := cat.DocumentChunks(ctx, "docs", "file:///a.md")
		if err != nil {
			t.Fatalf("DocumentChunks: %v", err)
		}
		if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
			t.Errorf("chunks = %+v, want first then second", got)
		}
	})

	t.Run("unknown document is ErrNotFound", func(t *testing.T) {
		if _, err := cat.DocumentChunks(ctx, "docs", "file:///ghost.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})
}

func TestCatalogChunksByIDs(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("a")), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	chunks := []domain.Chunk{mustChunk(t, docID, 0, "first"), mustChunk(t, docID, 1, "second")}
	docs := &fakeDocs{}
	if err := docs.Upsert(ctx, doc, chunks); err != nil {
		t.Fatal(err)
	}
	cat := app.NewCatalog(newFakeCollections(mustCollection(t, "docs", space)), docs, &fakeEmbedder{space: space}, chunker41(t))

	t.Run("returns requested chunks in input order, omitting absent", func(t *testing.T) {
		got, err := cat.ChunksByIDs(ctx, "docs", []string{string(chunks[1].ID), "no-such-chunk", string(chunks[0].ID)})
		if err != nil {
			t.Fatalf("ChunksByIDs: %v", err)
		}
		if len(got) != 2 || got[0].ID != chunks[1].ID || got[1].ID != chunks[0].ID {
			t.Errorf("chunks = %+v, want [second, first]", got)
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		if _, err := cat.ChunksByIDs(ctx, "missing", []string{string(chunks[0].ID)}); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})
}
