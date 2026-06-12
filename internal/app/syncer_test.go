package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestSyncer(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	// newSyncer wires a Syncer and its collaborators over shared fakes.
	newSyncer := func(coll *domain.Collection, src *fakeSource, docs *fakeDocs, idx *fakeIndex) *app.Syncer {
		colls := newFakeCollections(coll)
		emb := &fakeEmbedder{space: space}
		catalog := app.NewCatalog(colls, docs, emb)
		ingestor := app.NewIngestor(colls, docs, idx, emb, &fakeExtractor{}, src, chunker41(t))
		remover := app.NewRemover(colls, docs, idx)
		return app.NewSyncer(catalog, ingestor, remover, src)
	}

	seedDoc := func(t *testing.T, docs *fakeDocs, idx *fakeIndex, collection, uri string) {
		t.Helper()
		doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(uri)), time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("NewDocument: %v", err)
		}
		ch := mustChunk(t, doc.ID, 0, "seed text")
		if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := idx.Upsert(ctx, collection, []app.VectorEntry{{ChunkID: ch.ID, Vector: []float32{1, 0, 0}}}); err != nil {
			t.Fatalf("index Upsert: %v", err)
		}
	}

	t.Run("ingests the given path and reports a summary", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		sy := newSyncer(coll, src, &fakeDocs{}, &fakeIndex{})

		sum, err := sy.Sync(ctx, "docs", []string{"/root"}, false)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if sum.Added != 1 || sum.Pruned != 0 {
			t.Errorf("summary = %+v, want Added 1 Pruned 0", sum)
		}
	})

	t.Run("replays remembered sources when no path is given", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		coll.Sources = []string{"/remembered"}
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		docs := &fakeDocs{}
		sy := newSyncer(coll, src, docs, &fakeIndex{})

		sum, err := sy.Sync(ctx, "docs", nil, false)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if sum.Added != 1 {
			t.Errorf("summary = %+v, want Added 1", sum)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///a.txt"); err != nil {
			t.Errorf("remembered source not ingested: %v", err)
		}
	})

	t.Run("no path and no remembered source is ErrInvalidArgument", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		sy := newSyncer(coll, &fakeSource{}, &fakeDocs{}, &fakeIndex{})
		if _, err := sy.Sync(ctx, "docs", nil, false); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		sy := newSyncer(mustCollection(t, "docs", space), &fakeSource{}, &fakeDocs{}, &fakeIndex{})
		if _, err := sy.Sync(ctx, "missing", []string{"/root"}, false); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("--prune removes documents absent from the source", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		// b.txt exists in the collection but is no longer at the source.
		seedDoc(t, docs, idx, "docs", "file:///b.txt")
		// The source now yields only a.txt.
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		sy := newSyncer(coll, src, docs, idx)

		sum, err := sy.Sync(ctx, "docs", []string{"/root"}, true)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if sum.Pruned != 1 {
			t.Errorf("Pruned = %d, want 1", sum.Pruned)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///b.txt"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("b.txt should be pruned, got %v", err)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///a.txt"); err != nil {
			t.Errorf("a.txt should remain: %v", err)
		}
	})

	t.Run("without --prune, absent documents are kept", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		seedDoc(t, docs, idx, "docs", "file:///b.txt")
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		sy := newSyncer(coll, src, docs, idx)

		sum, err := sy.Sync(ctx, "docs", []string{"/root"}, false)
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if sum.Pruned != 0 {
			t.Errorf("Pruned = %d, want 0 without --prune", sum.Pruned)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///b.txt"); err != nil {
			t.Errorf("b.txt must be kept without --prune: %v", err)
		}
	})
}
