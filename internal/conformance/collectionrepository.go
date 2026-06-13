package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// RunCollectionRepositorySuite verifies the app.CollectionRepository contract.
// The factory must return a fresh, empty repository per call.
//
// This suite covers only what the port can observe: Collection CRUD. The
// cross-aggregate cascade of Delete (removing the collection's documents and
// vectors) is orchestrated by the use case, which holds the
// DocumentRepository and VectorIndex; it is verified at that layer.
func RunCollectionRepositorySuite(t *testing.T, factory func(t *testing.T) app.CollectionRepository) {
	t.Helper()
	ctx := context.Background()

	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	space, err := domain.NewEmbeddingSpace("text-embedding-3-small", 1536)
	if err != nil {
		t.Fatalf("NewEmbeddingSpace: %v", err)
	}
	spec, err := domain.NewChunkerSpec("structure", 1, 512, 64, "o200k_base", true)
	if err != nil {
		t.Fatalf("NewChunkerSpec: %v", err)
	}
	newCollection := func(t *testing.T, name string) *domain.Collection {
		t.Helper()
		c, err := domain.NewCollection(name, space, spec, createdAt)
		if err != nil {
			t.Fatalf("NewCollection(%q): %v", name, err)
		}
		return c
	}

	t.Run("create then get round-trips", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Create(ctx, newCollection(t, "docs")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := repo.Get(ctx, "docs")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "docs" || !got.Space.Equal(space) || !got.CreatedAt.Equal(createdAt) {
			t.Errorf("round-trip mismatch: got %+v", got)
		}
		// The chunker pin must survive persistence — a collection that lost its
		// spec would silently become unpinned (and refuse re-ingest).
		if got.Chunker != spec {
			t.Errorf("chunker spec round-trip mismatch: got %+v, want %+v", got.Chunker, spec)
		}
	})

	t.Run("create rejects a duplicate name with ErrAlreadyExists", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Create(ctx, newCollection(t, "docs")); err != nil {
			t.Fatalf("first Create: %v", err)
		}
		if err := repo.Create(ctx, newCollection(t, "docs")); !errors.Is(err, app.ErrAlreadyExists) {
			t.Errorf("want ErrAlreadyExists, got %v", err)
		}
	})

	t.Run("get unknown returns ErrNotFound", func(t *testing.T) {
		repo := factory(t)
		if _, err := repo.Get(ctx, "nope"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("list returns every collection", func(t *testing.T) {
		repo := factory(t)
		want := []string{"docs", "notes", "wiki"}
		for _, name := range want {
			if err := repo.Create(ctx, newCollection(t, name)); err != nil {
				t.Fatalf("Create(%q): %v", name, err)
			}
		}
		got, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("want %d collections, got %d", len(want), len(got))
		}
		names := make(map[string]bool, len(got))
		for _, c := range got {
			names[c.Name] = true
		}
		for _, name := range want {
			if !names[name] {
				t.Errorf("List missing %q; got %v", name, names)
			}
		}
	})

	t.Run("list of an empty repository is empty, no error", func(t *testing.T) {
		repo := factory(t)
		got, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("want empty, got %v", got)
		}
	})

	t.Run("delete removes the collection", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Create(ctx, newCollection(t, "docs")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := repo.Delete(ctx, "docs"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := repo.Get(ctx, "docs"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("after Delete, Get: want ErrNotFound, got %v", err)
		}
	})

	t.Run("delete unknown returns ErrNotFound", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Delete(ctx, "nope"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("record source appends to Get, idempotently", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Create(ctx, newCollection(t, "docs")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := repo.RecordSource(ctx, "docs", "/a"); err != nil {
			t.Fatalf("RecordSource /a: %v", err)
		}
		if err := repo.RecordSource(ctx, "docs", "/b"); err != nil {
			t.Fatalf("RecordSource /b: %v", err)
		}
		if err := repo.RecordSource(ctx, "docs", "/a"); err != nil { // duplicate
			t.Fatalf("RecordSource /a again: %v", err)
		}

		got, err := repo.Get(ctx, "docs")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		set := make(map[string]int, len(got.Sources))
		for _, s := range got.Sources {
			set[s]++
		}
		if len(got.Sources) != 2 || set["/a"] != 1 || set["/b"] != 1 {
			t.Errorf("want sources {/a,/b} without duplicates, got %v", got.Sources)
		}
	})

	t.Run("record source on unknown collection is ErrNotFound", func(t *testing.T) {
		repo := factory(t)
		if err := repo.RecordSource(ctx, "nope", "/a"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("a fresh collection has no sources", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Create(ctx, newCollection(t, "docs")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := repo.Get(ctx, "docs")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Sources) != 0 {
			t.Errorf("want no sources, got %v", got.Sources)
		}
	})

	t.Run("stored collection is independent of the caller's struct", func(t *testing.T) {
		repo := factory(t)
		c := newCollection(t, "docs")
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create: %v", err)
		}
		c.Name = "mutated"
		c.CreatedAt = createdAt.Add(time.Hour)

		got, err := repo.Get(ctx, "docs")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "docs" || !got.CreatedAt.Equal(createdAt) {
			t.Errorf("repository must not alias the caller's struct; got %+v", got)
		}
	})
}
