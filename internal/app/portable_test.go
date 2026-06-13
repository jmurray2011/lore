package app_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/artifact"
	"github.com/jmurray2011/lore/internal/domain"
)

// seedExportable populates the fakes with one collection (pins + two sources),
// one document, and two chunks with vectors — enough to exercise a round-trip.
func seedExportable(t *testing.T, colls *fakeCollections, docs *fakeDocs, idx *fakeIndex, name string) {
	t.Helper()
	ctx := context.Background()
	coll := mustCollection(t, name, testSpace())
	coll.Sources = []string{"file:///docs", "file:///more"}
	if err := colls.Create(ctx, coll); err != nil {
		t.Fatal(err)
	}
	doc, err := domain.NewDocument(name, "file:///docs/a.md", domain.HashContent([]byte("a")), time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	doc.Fingerprint = "fp-a"
	c0 := domain.Chunk{ID: domain.DeriveChunkID(doc.ID, 0), DocumentID: doc.ID, Seq: 0, Text: "alpha", HeadingPath: "Intro"}
	c1 := domain.Chunk{ID: domain.DeriveChunkID(doc.ID, 1), DocumentID: doc.ID, Seq: 1, Text: "beta"}
	if err := docs.Upsert(ctx, doc, []domain.Chunk{c0, c1}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, name, []app.VectorEntry{
		{ChunkID: c0.ID, Vector: []float32{1, 0, 0}},
		{ChunkID: c1.ID, Vector: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatal(err)
	}
}

func newImporter(colls *fakeCollections, docs *fakeDocs, idx *fakeIndex) *app.Importer {
	return app.NewImporter(colls, docs, idx, app.NewRemover(colls, docs, idx))
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	srcColls, srcDocs, srcIdx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	seedExportable(t, srcColls, srcDocs, srcIdx, "kb")

	var buf bytes.Buffer
	sum, err := app.NewExporter(srcColls, srcDocs, srcIdx).Export(ctx, "kb", &buf)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if sum.Documents != 1 || sum.Chunks != 2 || sum.Model != testSpace().Model || sum.Dimensions != testSpace().Dimensions {
		t.Errorf("export summary = %+v", sum)
	}

	dstColls, dstDocs, dstIdx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	isum, err := newImporter(dstColls, dstDocs, dstIdx).Import(ctx, &buf, "", false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if isum.Collection != "kb" || isum.Documents != 1 || isum.Chunks != 2 {
		t.Errorf("import summary = %+v", isum)
	}

	// Collection pins + metadata identical.
	got, err := dstColls.Get(ctx, "kb")
	if err != nil {
		t.Fatalf("Get imported: %v", err)
	}
	if !got.Space.Equal(testSpace()) || got.Chunker != testChunkerSpec() {
		t.Errorf("pins not preserved: space=%v chunker=%v", got.Space, got.Chunker)
	}
	if !slices.Equal(got.Sources, []string{"file:///docs", "file:///more"}) {
		t.Errorf("sources not preserved: %v", got.Sources)
	}

	// Documents, chunks, and vectors identical (IDs re-derive to the same values
	// because the name is unchanged).
	docID := domain.DeriveDocumentID("kb", "file:///docs/a.md")
	chunks, err := dstDocs.GetChunksByDocument(ctx, "kb", docID)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("imported chunks: %v, %v", chunks, err)
	}
	if chunks[0].Text != "alpha" || chunks[0].HeadingPath != "Intro" || chunks[1].Text != "beta" {
		t.Errorf("chunk content not preserved: %+v", chunks)
	}
	entries, _ := dstIdx.Entries(ctx, "kb")
	if len(entries) != 2 {
		t.Fatalf("want 2 imported vectors, got %d", len(entries))
	}
	byID := map[domain.ChunkID][]float32{}
	for _, e := range entries {
		byID[e.ChunkID] = e.Vector
	}
	if !slices.Equal(byID[chunks[0].ID], []float32{1, 0, 0}) || !slices.Equal(byID[chunks[1].ID], []float32{0, 1, 0}) {
		t.Errorf("vectors not preserved: %v", byID)
	}
}

func TestImportRename(t *testing.T) {
	ctx := context.Background()
	srcColls, srcDocs, srcIdx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	seedExportable(t, srcColls, srcDocs, srcIdx, "kb")
	var buf bytes.Buffer
	if _, err := app.NewExporter(srcColls, srcDocs, srcIdx).Export(ctx, "kb", &buf); err != nil {
		t.Fatal(err)
	}

	dstColls, dstDocs, dstIdx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	if _, err := newImporter(dstColls, dstDocs, dstIdx).Import(ctx, &buf, "renamed", false); err != nil {
		t.Fatalf("Import --name: %v", err)
	}
	if _, err := dstColls.Get(ctx, "renamed"); err != nil {
		t.Errorf("renamed collection missing: %v", err)
	}
	// IDs must re-derive for the new name, and the vectors must be keyed to the
	// new chunk IDs (so a later query/add into the renamed collection is coherent).
	docID := domain.DeriveDocumentID("renamed", "file:///docs/a.md")
	chunks, err := dstDocs.GetChunksByDocument(ctx, "renamed", docID)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("renamed chunks: %v, %v", chunks, err)
	}
	entries, _ := dstIdx.Entries(ctx, "renamed")
	have := map[domain.ChunkID]bool{}
	for _, e := range entries {
		have[e.ChunkID] = true
	}
	for _, c := range chunks {
		if !have[c.ID] {
			t.Errorf("vector missing for re-derived chunk %s", c.ID)
		}
	}
}

func TestImportCollisionAndForce(t *testing.T) {
	ctx := context.Background()
	export := func() *bytes.Buffer {
		c, d, i := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
		seedExportable(t, c, d, i, "kb")
		var buf bytes.Buffer
		if _, err := app.NewExporter(c, d, i).Export(ctx, "kb", &buf); err != nil {
			t.Fatal(err)
		}
		return &buf
	}

	t.Run("refuses an existing name without force", func(t *testing.T) {
		colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
		seedExportable(t, colls, docs, idx, "kb") // already present
		_, err := newImporter(colls, docs, idx).Import(ctx, export(), "", false)
		if !errors.Is(err, app.ErrAlreadyExists) {
			t.Errorf("want ErrAlreadyExists, got %v", err)
		}
	})

	t.Run("force replaces the existing collection", func(t *testing.T) {
		colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
		// Pre-existing "kb" with a stale document that must be gone after force.
		stale := mustCollection(t, "kb", testSpace())
		if err := colls.Create(ctx, stale); err != nil {
			t.Fatal(err)
		}
		staleDoc, _ := domain.NewDocument("kb", "file:///stale.md", domain.HashContent([]byte("s")), time.Unix(1, 0).UTC())
		sc := domain.Chunk{ID: domain.DeriveChunkID(staleDoc.ID, 0), DocumentID: staleDoc.ID, Seq: 0, Text: "stale"}
		if err := docs.Upsert(ctx, staleDoc, []domain.Chunk{sc}); err != nil {
			t.Fatal(err)
		}

		if _, err := newImporter(colls, docs, idx).Import(ctx, export(), "", true); err != nil {
			t.Fatalf("Import --force: %v", err)
		}
		// The stale document is gone; the imported one is present.
		if _, err := docs.GetBySource(ctx, "kb", "file:///stale.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("stale document survived force-import: %v", err)
		}
		if _, err := docs.GetBySource(ctx, "kb", "file:///docs/a.md"); err != nil {
			t.Errorf("imported document missing after force: %v", err)
		}
	})
}

func TestImportRejectsNewerArtifact(t *testing.T) {
	ctx := context.Background()
	// A frame with a version newer than this binary understands.
	var buf bytes.Buffer
	buf.WriteString(artifact.Magic)
	buf.Write([]byte{0, 0, 0, byte(artifact.FormatVersion + 1)})
	buf.WriteString("body")

	colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	_, err := newImporter(colls, docs, idx).Import(ctx, &buf, "", false)
	if !errors.Is(err, artifact.ErrUnsupportedVersion) {
		t.Fatalf("want ErrUnsupportedVersion, got %v", err)
	}
	if cs, _ := colls.List(ctx); len(cs) != 0 {
		t.Errorf("nothing should be imported on a version error, got %d collections", len(cs))
	}
}

func TestExportUnknownCollection(t *testing.T) {
	colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	if _, err := app.NewExporter(colls, docs, idx).Export(context.Background(), "ghost", &bytes.Buffer{}); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
