package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestRemover(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	// seed stores a document with nChunks chunks plus their vectors.
	seed := func(t *testing.T, docs *fakeDocs, idx *fakeIndex, collection, uri string, nChunks int) {
		t.Helper()
		doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(uri)), time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		chunks := make([]domain.Chunk, nChunks)
		entries := make([]app.VectorEntry, nChunks)
		for i := range chunks {
			ch, err := domain.NewChunk(doc.ID, i, fmt.Sprintf("chunk %d", i))
			if err != nil {
				t.Fatal(err)
			}
			chunks[i] = ch
			entries[i] = app.VectorEntry{ChunkID: ch.ID, Vector: []float32{1, 0, 0}}
		}
		if err := docs.Upsert(ctx, doc, chunks); err != nil {
			t.Fatal(err)
		}
		if err := idx.Upsert(ctx, collection, entries); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("RemoveCollection deletes documents, vectors, and the collection record", func(t *testing.T) {
		colls := newFakeCollections(mustCollection(t, "docs", space))
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		seed(t, docs, idx, "docs", "file:///a.md", 2)
		seed(t, docs, idx, "docs", "file:///b.md", 3)
		rm := app.NewRemover(colls, docs, idx)

		if err := rm.RemoveCollection(ctx, "docs"); err != nil {
			t.Fatalf("RemoveCollection: %v", err)
		}
		if _, err := colls.Get(ctx, "docs"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("collection record not removed: %v", err)
		}
		if idx.count("docs") != 0 {
			t.Errorf("vectors remain: %d", idx.count("docs"))
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///a.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("document remains: %v", err)
		}
	})

	t.Run("RemoveCollection unknown is ErrNotFound", func(t *testing.T) {
		rm := app.NewRemover(newFakeCollections(), &fakeDocs{}, &fakeIndex{})
		if err := rm.RemoveCollection(ctx, "ghost"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("RemoveDocument deletes one document and its vectors, leaving others", func(t *testing.T) {
		colls := newFakeCollections(mustCollection(t, "docs", space))
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		seed(t, docs, idx, "docs", "file:///a.md", 2)
		seed(t, docs, idx, "docs", "file:///b.md", 1) // survivor
		rm := app.NewRemover(colls, docs, idx)

		if err := rm.RemoveDocument(ctx, "docs", "file:///a.md"); err != nil {
			t.Fatalf("RemoveDocument: %v", err)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///a.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("document a remains: %v", err)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///b.md"); err != nil {
			t.Errorf("document b should survive: %v", err)
		}
		if idx.count("docs") != 1 {
			t.Errorf("want 1 surviving vector, got %d", idx.count("docs"))
		}
	})

	t.Run("RemoveDocument unknown is ErrNotFound", func(t *testing.T) {
		rm := app.NewRemover(newFakeCollections(mustCollection(t, "docs", space)), &fakeDocs{}, &fakeIndex{})
		if err := rm.RemoveDocument(ctx, "docs", "file:///missing.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})
}
