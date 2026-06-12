package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestCatalogInit(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	t.Run("creates a collection pinned to the embedder space", func(t *testing.T) {
		colls := newFakeCollections()
		cat := app.NewCatalog(colls, &fakeEmbedder{space: space})

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
		cat := app.NewCatalog(newFakeCollections(), &fakeEmbedder{space: space})
		if _, err := cat.Init(ctx, "Bad Name"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("duplicate is ErrAlreadyExists", func(t *testing.T) {
		colls := newFakeCollections()
		cat := app.NewCatalog(colls, &fakeEmbedder{space: space})
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
	cat := app.NewCatalog(newFakeCollections(), &fakeEmbedder{space: testSpace()})
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
