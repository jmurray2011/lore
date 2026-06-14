package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// chunker41 is a Registry whose default is a fixed 4/1-word chunker, the legacy
// behavior the ingestion tests assert against. Its spec is testChunkerSpec, so
// collections built by mustCollection accept ingestion through it.
func chunker41(t *testing.T) domain.Registry {
	t.Helper()
	c, err := domain.NewFixedChunker(4, 1)
	if err != nil {
		t.Fatalf("NewFixedChunker: %v", err)
	}
	reg, err := domain.NewRegistry(testChunkerSpec(), c, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

// textItem builds a text SourceItem whose fingerprint tracks its content, so
// re-yielding the same content fast-skips and changed content does not.
func textItem(uri, content string) app.SourceItem {
	b := []byte(content)
	return app.SourceItem{
		URI:         uri,
		ContentType: "text/plain",
		Fingerprint: fmt.Sprintf("%d:%s", len(b), content),
		Open:        func() ([]byte, error) { return b, nil },
	}
}

func TestIngestorAttachesMetadata(t *testing.T) {
	ctx := context.Background()
	space := testSpace()

	t.Run("user metadata reaches the document and every vector entry", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		ing := app.NewIngestor(newFakeCollections(coll), docs, idx, &fakeEmbedder{space: space}, &fakeExtractor{}, src, chunker41(t))

		meta := domain.Metadata{"author": "alice", "team": "platform"}
		if _, err := ing.Ingest(ctx, "docs", "/root", app.WithMeta(meta)); err != nil {
			t.Fatalf("Ingest: %v", err)
		}

		got, err := docs.GetBySource(ctx, "docs", "file:///a.txt")
		if err != nil {
			t.Fatalf("GetBySource: %v", err)
		}
		if got.Metadata["author"] != "alice" || got.Metadata["team"] != "platform" {
			t.Errorf("document metadata = %v, want author=alice team=platform", got.Metadata)
		}
		if len(idx.gotEntries) == 0 {
			t.Fatal("no vector entries upserted")
		}
		for _, e := range idx.gotEntries {
			if e.Metadata["author"] != "alice" {
				t.Errorf("vector entry %s missing metadata for --where filtering: %v", e.ChunkID, e.Metadata)
			}
		}
	})

	t.Run("markdown front matter is parsed, overridden by user meta, and kept out of chunks", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		docs := &fakeDocs{}
		idx := &fakeIndex{}
		content := "---\nauthor: frontmatter\ndate: 2025-06-01\n---\n" + words(8)
		md := app.SourceItem{
			URI:         "file:///note.md",
			ContentType: "text/markdown",
			Fingerprint: "fm",
			Open:        func() ([]byte, error) { return []byte(content), nil },
		}
		src := &fakeSource{items: []app.SourceItem{md}}
		ing := app.NewIngestor(newFakeCollections(coll), docs, idx, &fakeEmbedder{space: space}, &fakeExtractor{}, src, chunker41(t))

		if _, err := ing.Ingest(ctx, "docs", "/root", app.WithMeta(domain.Metadata{"author": "alice"})); err != nil {
			t.Fatalf("Ingest: %v", err)
		}

		got, err := docs.GetBySource(ctx, "docs", "file:///note.md")
		if err != nil {
			t.Fatalf("GetBySource: %v", err)
		}
		if got.Metadata["author"] != "alice" {
			t.Errorf("user --meta must override front matter, got author=%q", got.Metadata["author"])
		}
		if got.Metadata["date"] != "2025-06-01" {
			t.Errorf("front-matter date should survive merge: %v", got.Metadata)
		}

		stored, err := docs.GetChunksByDocument(ctx, "docs", domain.DeriveDocumentID("docs", "file:///note.md"))
		if err != nil {
			t.Fatalf("GetChunksByDocument: %v", err)
		}
		if len(stored) == 0 {
			t.Fatal("no chunks stored")
		}
		for _, c := range stored {
			if strings.Contains(c.Text, "author:") || strings.Contains(c.Text, "---") {
				t.Errorf("front matter leaked into a chunk: %q", c.Text)
			}
		}
	})
}

func TestIngestorChunkerGuard(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}

	ingest := func(coll *domain.Collection) error {
		ing := app.NewIngestor(newFakeCollections(coll), &fakeDocs{}, &fakeIndex{}, &fakeEmbedder{space: space}, &fakeExtractor{}, src, chunker41(t))
		_, err := ing.Ingest(ctx, "docs", "/root")
		return err
	}

	t.Run("re-ingest under a different chunker spec is refused", func(t *testing.T) {
		// Pinned to a different size than the ingestor's chunker (chunker41 = 4/1).
		other, err := domain.NewChunkerSpec("fixed", domain.FixedChunkerVersion, 8, 1, "words", false)
		if err != nil {
			t.Fatal(err)
		}
		coll, err := domain.NewCollection("docs", space, other, time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if err := ingest(coll); !errors.Is(err, domain.ErrChunkerMismatch) {
			t.Errorf("want ErrChunkerMismatch, got %v", err)
		}
	})

	t.Run("legacy unpinned collection is refused", func(t *testing.T) {
		legacy := &domain.Collection{Name: "docs", Space: space} // zero chunker spec, as loaded from a pre-pin DB
		if err := ingest(legacy); !errors.Is(err, domain.ErrChunkerMismatch) {
			t.Errorf("want ErrChunkerMismatch, got %v", err)
		}
	})
}

// prefixingChunker returns one chunk whose embedded text carries a context
// prefix the stored text does not — the slice-5 embed/store split.
type prefixingChunker struct{}

func (prefixingChunker) Chunk(domain.ParsedDoc) ([]domain.ChunkResult, error) {
	return []domain.ChunkResult{{
		Text:        "body words here",
		EmbedText:   "CTX\n\nbody words here",
		HeadingPath: "CTX",
	}}, nil
}

func TestIngestorEmbedsPrefixStoresOriginal(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	coll := mustCollection(t, "docs", space)
	reg, err := domain.NewRegistry(testChunkerSpec(), prefixingChunker{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{space: space}
	docs := &fakeDocs{}
	src := &fakeSource{items: []app.SourceItem{textItem("file:///a.md", "anything")}}
	ing := app.NewIngestor(newFakeCollections(coll), docs, &fakeIndex{}, emb, &fakeExtractor{}, src, reg)

	if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// The embedder must have received the prefixed text...
	if !slices.Contains(emb.embedded, "CTX\n\nbody words here") {
		t.Errorf("embedder did not receive the prefixed text: %v", emb.embedded)
	}
	// ...but the stored chunk keeps the original (so citations/inspection are clean).
	stored, err := docs.GetChunksByDocument(ctx, "docs", domain.DeriveDocumentID("docs", "file:///a.md"))
	if err != nil {
		t.Fatalf("GetChunksByDocument: %v", err)
	}
	if len(stored) != 1 || stored[0].Text != "body words here" {
		t.Errorf("stored chunk should hold the original un-prefixed text, got %+v", stored)
	}
	if stored[0].HeadingPath != "CTX" {
		t.Errorf("stored chunk heading path = %q, want CTX", stored[0].HeadingPath)
	}
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

	t.Run("records the source root on the collection", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		colls := newFakeCollections(coll)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}}
		ing := app.NewIngestor(colls, &fakeDocs{}, &fakeIndex{}, &fakeEmbedder{space: space}, &fakeExtractor{}, src, chunker41(t))

		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		// Re-ingesting the same root must not duplicate it.
		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("second Ingest: %v", err)
		}

		got, err := colls.Get(ctx, "docs")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Sources) != 1 || got.Sources[0] != "/root" {
			t.Errorf("want source root /root recorded once, got %v", got.Sources)
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

	t.Run("fast-skips a matching fingerprint without reading the file", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		var opens int
		mkItem := func(fp string) app.SourceItem {
			return app.SourceItem{URI: "file:///a.txt", ContentType: "text/plain", Fingerprint: fp,
				Open: func() ([]byte, error) { opens++; return []byte(words(10)), nil }}
		}
		src := &fakeSource{items: []app.SourceItem{mkItem("fp-1")}}
		ing := app.NewIngestor(newFakeCollections(coll), &fakeDocs{}, &fakeIndex{}, &fakeEmbedder{space: space}, &fakeExtractor{}, src, chunker41(t))

		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("first Ingest: %v", err)
		}
		if opens != 1 {
			t.Fatalf("first ingest should read once, opened %d", opens)
		}

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("second Ingest: %v", err)
		}
		if sum.Added != 0 || sum.Skipped != 1 {
			t.Errorf("want fast-skip (Added 0 Skipped 1), got %+v", sum)
		}
		if opens != 1 {
			t.Errorf("a matching fingerprint must not re-open the file; opened %d", opens)
		}
	})

	t.Run("refreshes a drifted fingerprint on unchanged content, then fast-skips", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		content := words(10)
		var opens int
		mkItem := func(fp string) app.SourceItem {
			return app.SourceItem{URI: "file:///a.txt", ContentType: "text/plain", Fingerprint: fp,
				Open: func() ([]byte, error) { opens++; return []byte(content), nil }}
		}
		src := &fakeSource{items: []app.SourceItem{mkItem("fp-1")}}
		ing := app.NewIngestor(newFakeCollections(coll), &fakeDocs{}, &fakeIndex{}, &fakeEmbedder{space: space}, &fakeExtractor{}, src, chunker41(t))

		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil { // stores fp-1, opens=1
			t.Fatalf("first Ingest: %v", err)
		}

		// Fingerprint drifts but content is identical: read once to verify, refresh.
		src.items[0] = mkItem("fp-2")
		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("second Ingest: %v", err)
		}
		if opens != 2 {
			t.Fatalf("a drifted fingerprint should re-read once, opened %d", opens)
		}

		// The refreshed fingerprint now matches, so the next run reads nothing.
		if _, err := ing.Ingest(ctx, "docs", "/root"); err != nil {
			t.Fatalf("third Ingest: %v", err)
		}
		if opens != 2 {
			t.Errorf("after refresh, matching fingerprint must fast-skip; opened %d", opens)
		}
	})

	t.Run("re-ingesting a shrunk document removes its stale tail vectors", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{textItem("file:///a.txt", words(10))}} // several chunks
		idx := &fakeIndex{}
		ing := newIngestor(coll, src, &fakeExtractor{}, &fakeEmbedder{space: space}, &fakeDocs{}, idx)

		first, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("first Ingest: %v", err)
		}
		if first.Chunks < 2 || idx.count("docs") != first.Chunks {
			t.Fatalf("setup: want several vectors == %d chunks, got %d", first.Chunks, idx.count("docs"))
		}

		// Shrink the document to a single chunk and re-ingest.
		src.items[0] = textItem("file:///a.txt", "single chunk x")
		second, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("second Ingest: %v", err)
		}
		if second.Added != 1 {
			t.Errorf("changed doc should be re-added: %+v", second)
		}
		if got := idx.count("docs"); got != second.Chunks {
			t.Errorf("stale tail vectors remain: index has %d, doc now has %d chunks", got, second.Chunks)
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

	t.Run("counts unsupported content types separately from skips", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		src := &fakeSource{items: []app.SourceItem{
			{URI: "file:///img.png", ContentType: "image/png", Open: func() ([]byte, error) { return []byte{0x89}, nil }},
			textItem("file:///a.txt", words(10)),
		}}
		ext := &fakeExtractor{unsupported: map[string]bool{"image/png": true}}
		ing := newIngestor(coll, src, ext, &fakeEmbedder{space: space}, &fakeDocs{}, &fakeIndex{})

		sum, err := ing.Ingest(ctx, "docs", "/root")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		// An unsupported type is never ingested — it must not hide under Skipped
		// (which means "ingested before, unchanged"); it's a distinct outcome.
		if sum.Added != 1 || sum.Unsupported != 1 || sum.Skipped != 0 {
			t.Errorf("summary = %+v, want Added 1 Unsupported 1 Skipped 0", sum)
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

	t.Run("IngestContent stores an in-memory document, idempotently", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		docs := &fakeDocs{}
		ing := newIngestor(coll, &fakeSource{}, &fakeExtractor{}, &fakeEmbedder{space: space}, docs, &fakeIndex{})

		sum, err := ing.IngestContent(ctx, "docs", "stdin:notes", "text/plain", []byte(words(10)))
		if err != nil {
			t.Fatalf("IngestContent: %v", err)
		}
		if sum.Added != 1 || sum.Chunks < 1 {
			t.Errorf("summary = %+v, want Added 1 with chunks", sum)
		}
		if _, err := docs.GetBySource(ctx, "docs", "stdin:notes"); err != nil {
			t.Errorf("document not stored: %v", err)
		}

		// Same content again is a no-op.
		again, err := ing.IngestContent(ctx, "docs", "stdin:notes", "text/plain", []byte(words(10)))
		if err != nil {
			t.Fatalf("IngestContent: %v", err)
		}
		if again.Added != 0 || again.Skipped != 1 {
			t.Errorf("re-ingest = %+v, want Added 0 Skipped 1", again)
		}
	})

	t.Run("IngestContent counts an unsupported type and records no sync source", func(t *testing.T) {
		coll := mustCollection(t, "docs", space)
		colls := newFakeCollections(coll)
		ext := &fakeExtractor{unsupported: map[string]bool{"image/png": true}}
		ing := app.NewIngestor(colls, &fakeDocs{}, &fakeIndex{}, &fakeEmbedder{space: space}, ext, &fakeSource{}, chunker41(t))

		sum, err := ing.IngestContent(ctx, "docs", "stdin", "image/png", []byte{0x89})
		if err != nil {
			t.Fatalf("IngestContent: %v", err)
		}
		if sum.Unsupported != 1 || sum.Added != 0 {
			t.Errorf("summary = %+v, want Unsupported 1 Added 0", sum)
		}
		got, _ := colls.Get(ctx, "docs")
		if len(got.Sources) != 0 {
			t.Errorf("stdin must not be recorded as a sync source, got %v", got.Sources)
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
