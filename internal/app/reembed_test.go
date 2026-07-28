package app_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// exportKB seeds and exports a collection in testSpace() (model "test-embed",
// dims 3), returning the artifact bytes.
func exportKB(t *testing.T) *bytes.Buffer {
	t.Helper()
	colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	seedExportable(t, colls, docs, idx, "kb")
	var buf bytes.Buffer
	if _, err := app.NewExporter(colls, docs, idx).Export(context.Background(), "kb", &buf); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestImportReEmbedRepinsToLocalSpace(t *testing.T) {
	ctx := context.Background()
	buf := exportKB(t)

	// A different local embedder: 2-dim space, with canned vectors for the chunk
	// texts ("alpha", "beta") the artifact carries.
	localSpace := domain.EmbeddingSpace{Model: "local-embed", Dimensions: 2}
	emb := &fakeEmbedder{space: localSpace, byText: map[string][]float32{
		"alpha": {0.5, 0.5},
		"beta":  {0.1, 0.9},
	}}
	colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	im := app.NewImporter(colls, docs, idx, app.NewRemover(colls, docs, idx, &fakeLexical{}), &fakeLexical{}, emb)

	sum, err := im.Import(ctx, buf, "", false, true) // reEmbed = true
	if err != nil {
		t.Fatalf("re-embed import: %v", err)
	}

	// The imported collection is pinned to the LOCAL space, not the artifact's.
	if sum.Model != "local-embed" || sum.Dimensions != 2 {
		t.Errorf("summary should report the local space, got %+v", sum)
	}
	got, err := colls.Get(ctx, "kb")
	if err != nil {
		t.Fatalf("Get imported: %v", err)
	}
	if !got.Space.Equal(localSpace) {
		t.Errorf("collection should be pinned to the local space, got %v", got.Space)
	}

	// Vectors are the re-embedded ones (2-dim, from the local embedder).
	entries, _ := idx.Entries(ctx, "kb")
	if len(entries) != 2 {
		t.Fatalf("want 2 re-embedded vectors, got %d", len(entries))
	}
	for _, e := range entries {
		if len(e.Vector) != localSpace.Dimensions {
			t.Errorf("vector %s should be in the local %d-dim space, got %d", e.ChunkID, localSpace.Dimensions, len(e.Vector))
		}
	}
	if emb.embedCalls.Load() == 0 {
		t.Error("re-embed must call the embedder to rebuild vectors")
	}
}

func TestImportReEmbedRequiresEmbedder(t *testing.T) {
	ctx := context.Background()
	buf := exportKB(t)
	colls, docs, idx := newFakeCollections(), &fakeDocs{}, &fakeIndex{}
	// No embedder wired.
	im := app.NewImporter(colls, docs, idx, app.NewRemover(colls, docs, idx, &fakeLexical{}), &fakeLexical{}, nil)
	if _, err := im.Import(ctx, buf, "", false, true); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("re-embed with no embedder should be a usage error, got %v", err)
	}
}
