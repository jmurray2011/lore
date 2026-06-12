package app_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// words builds "w0 w1 ... w(n-1)" — enough distinct tokens to drive the chunker.
func words(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "w" + strconv.Itoa(i)
	}
	return strings.Join(parts, " ")
}

func chunker41(t *testing.T) domain.Chunker {
	t.Helper()
	c, err := domain.NewChunker(4, 1)
	if err != nil {
		t.Fatalf("NewChunker: %v", err)
	}
	return c
}

func textItem(uri, content string) app.SourceItem {
	return app.SourceItem{URI: uri, ContentType: "text/plain", Content: []byte(content)}
}

func TestIngestor(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	newIngestor := func(coll *domain.Collection, src *fakeSource, ext *fakeExtractor, emb *fakeEmbedder, docs *fakeDocs, idx *fakeIndex) *app.Ingestor {
		return app.NewIngestor(newFakeCollections(coll), docs, idx, emb, ext, src, chunker41(t))
	}

	t.Run("ingests new documents, storing chunks and vectors", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{
			textItem("file:///a.txt", words(10)),        // 3 chunks
			textItem("file:///b.txt", "single chunk x"), // 1 chunk
		}}
		emb := &fakeEmbedder{space: space}
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		ing := newIngestor(coll, src, &fakeExtractor{}, emb, docs, idx)

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if sum.Added != 2 || sum.Skipped != 0 {
			t.Errorf("summary = %+v, want Added 2 Skipped 0", sum)
		}
		if sum.Chunks != 4 {
			t.Errorf("Chunks = %d, want 4", sum.Chunks)
		}
		if _, err := docs.GetBySource(ctx, "docs", "file:///a.txt"); err != nil {
			t.Errorf("document a not stored: %v", err)
		}
		if got := idx.count("docs"); got != sum.Chunks {
			t.Errorf("vectors stored = %d, want %d", got, sum.Chunks)
		}
	})

	t.Run("is idempotent: unchanged content skips and does not re-embed", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		emb := &fakeEmbedder{space: space}
		ing := newIngestor(coll, src, &fakeExtractor{}, emb, &fakeDocs{}, &fakeIndex{})

		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("first Ingest: %v", err)
		}
		afterFirst := emb.embedCalls.Load()
		if afterFirst == 0 {
			t.Fatal("expected embedding on first ingest")
		}

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("second Ingest: %v", err)
		}
		if sum.Added != 0 || sum.Skipped != 1 {
			t.Errorf("re-ingest summary = %+v, want Added 0 Skipped 1", sum)
		}
		if emb.embedCalls.Load() != afterFirst {
			t.Errorf("re-ingest must not embed again: calls %d -> %d", afterFirst, emb.embedCalls.Load())
		}
	})

	t.Run("re-ingests changed content", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		docs := &fakeDocs{}
		ing := newIngestor(coll, src, &fakeExtractor{}, &fakeEmbedder{space: space}, docs, &fakeIndex{})

		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("first Ingest: %v", err)
		}
		first, _ := docs.GetBySource(ctx, "docs", "file:///a.txt")

		src.items[0] = textItem("file:///a.txt", "completely different words appear now")
		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("second Ingest: %v", err)
		}
		if sum.Added != 1 {
			t.Errorf("changed doc should be re-added: summary %+v", sum)
		}
		second, _ := docs.GetBySource(ctx, "docs", "file:///a.txt")
		if second.Hash == first.Hash {
			t.Error("hash must change after content change")
		}
	})

	t.Run("skips unsupported content types", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{
			{URI: "file:///img.png", ContentType: "image/png", Content: []byte{0x89}},
			textItem("file:///a.txt", words(10)),
		}}
		ext := &fakeExtractor{unsupported: map[string]bool{"image/png": true}}
		ing := newIngestor(coll, src, ext, &fakeEmbedder{space: space}, &fakeDocs{}, &fakeIndex{})

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if sum.Added != 1 || sum.Skipped != 1 {
			t.Errorf("summary = %+v, want Added 1 Skipped 1", sum)
		}
	})

	t.Run("skips documents that produce no chunks", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///empty.txt", "   \n\t  ")}}
		ing := newIngestor(coll, src, &fakeExtractor{}, &fakeEmbedder{space: space}, &fakeDocs{}, &fakeIndex{})

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if sum.Added != 0 || sum.Skipped != 1 {
			t.Errorf("summary = %+v, want Added 0 Skipped 1", sum)
		}
	})

	t.Run("space mismatch is ErrSpaceMismatch and ingests nothing", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		emb := &fakeEmbedder{space: domain.EmbeddingSpace{Model: "other", Dimensions: 9}}
		ing := newIngestor(coll, src, &fakeExtractor{}, emb, &fakeDocs{}, &fakeIndex{})

		if _, err := ing.Ingest(ctx, "docs", "/root"); !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Errorf("want ErrSpaceMismatch, got %v", err)
		}
		if emb.embedCalls.Load() != 0 {
			t.Error("must not embed on space mismatch")
		}
	})

	t.Run("unknown collection is ErrNotFound", func(t *testing.T) {
		ing := app.NewIngestor(newFakeCollections(), &fakeDocs{}, &fakeIndex{}, &fakeEmbedder{space: space}, &fakeExtractor{}, &fakeSource{}, chunker41(t))
		if _, err := ing.Ingest(ctx, "missing", "/root"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("propagates embedder errors (fail-fast)", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		boom := errors.New("embed boom")
		emb := &fakeEmbedder{space: space, embedErr: boom}
		ing := newIngestor(coll, src, &fakeExtractor{}, emb, &fakeDocs{}, &fakeIndex{})

		if _, err := ing.Ingest(ctx, "docs", "/root"); !errors.Is(err, boom) {
			t.Errorf("want wrapped embed error, got %v", err)
		}
	})

	t.Run("WithConcurrency bounds parallel embeds", func(t *testing.T) {
		const limit = 2
		coll := mustCollection(t, "docs", space)
		items := make([]app.SourceItem, 6)
		for i := range items {
			items[i] = textItem(fmt.Sprintf("file:///c-%d.txt", i), words(10))
		}
		src := &fakeSource{items: items}

		entered := make(chan struct{}, len(items))
		release := make(chan struct{})
		emb := &fakeEmbedder{space: space, onEmbed: func() {
			entered <- struct{}{}
			<-release
		}}
		ing := app.NewIngestor(newFakeCollections(coll), &fakeDocs{}, &fakeIndex{}, emb, &fakeExtractor{}, src, chunker41(t), app.WithConcurrency(limit))

		done := make(chan error, 1)
		go func() { _, err := ing.Ingest(ctx, "docs", "/root"); done <- err }()

		// Wait until `limit` embeds are parked (proves at least `limit` ran at once).
		for i := 0; i < limit; i++ {
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatalf("only %d concurrent embeds reached, want %d", i, limit)
			}
		}
		// No further embed may start while `limit` are parked.
		select {
		case <-entered:
			close(release)
			t.Fatal("concurrency exceeded the configured limit")
		case <-time.After(100 * time.Millisecond):
		}

		close(release)
		if err := <-done; err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	})

	t.Run("ingests many documents concurrently", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		items := make([]app.SourceItem, 50)
		for i := range items {
			items[i] = textItem(fmt.Sprintf("file:///doc-%d.txt", i), words(10))
		}
		src := &fakeSource{items: items}
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		ing := newIngestor(coll, src, &fakeExtractor{}, &fakeEmbedder{space: space}, docs, idx)

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if sum.Added != 50 {
			t.Errorf("Added = %d, want 50", sum.Added)
		}
		if idx.count("docs") != sum.Chunks {
			t.Errorf("vectors stored = %d, want %d", idx.count("docs"), sum.Chunks)
		}
	})
}
