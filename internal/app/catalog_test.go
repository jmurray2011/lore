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
		cat := app.NewCatalog(colls, &fakeDocs{}, &fakeEmbedder{space: space})

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
		cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: space})
		if _, err := cat.Init(ctx, "Bad Name"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("duplicate is ErrAlreadyExists", func(t *testing.T) {
		colls := newFakeCollections()
		cat := app.NewCatalog(colls, &fakeDocs{}, &fakeEmbedder{space: space})
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
	cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: testSpace()})
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
		cat := app.NewCatalog(newFakeCollections(mustCollection(t, "docs", space)), docs, &fakeEmbedder{space: space})

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
		cat := app.NewCatalog(newFakeCollections(), &fakeDocs{}, &fakeEmbedder{space: space})
		if _, err := cat.ListDocuments(ctx, "missing"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})
}
