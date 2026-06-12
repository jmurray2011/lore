package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// RunDocumentRepositorySuite verifies the app.DocumentRepository contract. The
// factory must return a fresh, empty repository per call.
//
// The within-port cascade (deleting a Document deletes its Chunks, invariant 3)
// is covered here. The Chunks' vectors live in the VectorIndex; removing those
// is the use case's job and is verified at that layer.
func RunDocumentRepositorySuite(t *testing.T, factory func(t *testing.T) app.DocumentRepository) {
	t.Helper()
	ctx := context.Background()
	ingestedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	newDoc := func(t *testing.T, collection, uri, content string, nChunks int) (*domain.Document, []domain.Chunk) {
		t.Helper()
		doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(content)), ingestedAt)
		if err != nil {
			t.Fatalf("NewDocument: %v", err)
		}
		chunks := make([]domain.Chunk, nChunks)
		for i := range chunks {
			ch, err := domain.NewChunk(doc.ID, i, fmt.Sprintf("%s chunk %d", content, i))
			if err != nil {
				t.Fatalf("NewChunk: %v", err)
			}
			chunks[i] = ch
		}
		return doc, chunks
	}

	mustUpsert := func(t *testing.T, repo app.DocumentRepository, doc *domain.Document, chunks []domain.Chunk) {
		t.Helper()
		if err := repo.Upsert(ctx, doc, chunks); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	t.Run("upsert then get by source round-trips", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 2)
		mustUpsert(t, repo, doc, chunks)

		got, err := repo.GetBySource(ctx, "docs", "file:///a.md")
		if err != nil {
			t.Fatalf("GetBySource: %v", err)
		}
		if got.ID != doc.ID || got.Hash != doc.Hash || got.SourceURI != doc.SourceURI {
			t.Errorf("round-trip mismatch: got %+v want %+v", got, doc)
		}
	})

	t.Run("get by source unknown returns ErrNotFound", func(t *testing.T) {
		repo := factory(t)
		if _, err := repo.GetBySource(ctx, "docs", "file:///missing.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("get chunks preserves input order and skips missing IDs", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 3)
		mustUpsert(t, repo, doc, chunks)

		ids := []domain.ChunkID{chunks[2].ID, "missing", chunks[0].ID}
		got, err := repo.GetChunks(ctx, ids)
		if err != nil {
			t.Fatalf("GetChunks: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 chunks (missing skipped), got %d", len(got))
		}
		if got[0].ID != chunks[2].ID || got[1].ID != chunks[0].ID {
			t.Errorf("input order not preserved: got [%s %s]", got[0].ID, got[1].ID)
		}
	})

	t.Run("upsert replaces a document's chunks", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 3)
		mustUpsert(t, repo, doc, chunks)

		// Re-ingest the same source with fewer chunks; the old ones must go.
		doc2, chunks2 := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		mustUpsert(t, repo, doc2, chunks2)

		got, err := repo.GetChunks(ctx, []domain.ChunkID{chunks[0].ID, chunks[1].ID, chunks[2].ID})
		if err != nil {
			t.Fatalf("GetChunks: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("re-upsert must replace chunks, not merge: got %d", len(got))
		}
		if got[0].ID != chunks[0].ID {
			t.Errorf("want surviving chunk %s, got %s", chunks[0].ID, got[0].ID)
		}
	})

	t.Run("documents are isolated by collection", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		docB, chunksB := newDoc(t, "notes", "file:///a.md", "beta", 1)
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, docB, chunksB)

		got, err := repo.GetBySource(ctx, "notes", "file:///a.md")
		if err != nil {
			t.Fatalf("GetBySource: %v", err)
		}
		if got.ID != docB.ID || got.ID == docA.ID {
			t.Errorf("same source in different collections must be distinct documents")
		}
	})

	t.Run("delete removes the document, cascades to chunks, and returns their IDs", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 3)
		mustUpsert(t, repo, doc, chunks)

		ids, err := repo.Delete(ctx, "docs", doc.ID)
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
		assertSameChunkIDs(t, ids, []domain.ChunkID{chunks[0].ID, chunks[1].ID, chunks[2].ID})
		if _, err := repo.GetBySource(ctx, "docs", "file:///a.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("after Delete, GetBySource: want ErrNotFound, got %v", err)
		}
		got, err := repo.GetChunks(ctx, ids)
		if err != nil {
			t.Fatalf("GetChunks: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("deleting a document must delete its chunks (invariant 3); got %d", len(got))
		}
	})

	t.Run("delete unknown returns ErrNotFound and no IDs", func(t *testing.T) {
		repo := factory(t)
		id := domain.DeriveDocumentID("docs", "file:///missing.md")
		ids, err := repo.Delete(ctx, "docs", id)
		if !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
		if len(ids) != 0 {
			t.Errorf("want no IDs on failure, got %v", ids)
		}
	})

	t.Run("delete collection removes every document and returns all chunk IDs", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 2)
		docB, chunksB := newDoc(t, "docs", "file:///b.md", "beta", 3)
		other, otherChunks := newDoc(t, "notes", "file:///c.md", "gamma", 1)
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, docB, chunksB)
		mustUpsert(t, repo, other, otherChunks)

		ids, err := repo.DeleteCollection(ctx, "docs")
		if err != nil {
			t.Fatalf("DeleteCollection: %v", err)
		}
		assertSameChunkIDs(t, ids, []domain.ChunkID{chunksA[0].ID, chunksA[1].ID, chunksB[0].ID, chunksB[1].ID, chunksB[2].ID})
		if _, err := repo.GetBySource(ctx, "docs", "file:///a.md"); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("document A should be gone, got %v", err)
		}
		if _, err := repo.GetBySource(ctx, "notes", "file:///c.md"); err != nil {
			t.Errorf("delete collection must not touch another collection: %v", err)
		}
	})

	t.Run("delete collection with no documents returns no IDs, no error", func(t *testing.T) {
		repo := factory(t)
		ids, err := repo.DeleteCollection(ctx, "empty")
		if err != nil || len(ids) != 0 {
			t.Errorf("want empty, nil; got %v, %v", ids, err)
		}
	})

	t.Run("stored chunks are independent of the caller's slice", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 2)
		orig := chunks[0]
		mustUpsert(t, repo, doc, chunks)

		// Reassign a slice element after Upsert: an aliasing store would be
		// corrupted. The decoy shares orig's ID (same doc, same Seq).
		decoy, err := domain.NewChunk(doc.ID, 0, "decoy text")
		if err != nil {
			t.Fatalf("NewChunk: %v", err)
		}
		chunks[0] = decoy

		got, err := repo.GetChunks(ctx, []domain.ChunkID{orig.ID})
		if err != nil {
			t.Fatalf("GetChunks: %v", err)
		}
		if len(got) != 1 || got[0].Text != orig.Text {
			t.Errorf("repository must not alias the caller's slice; got %+v", got)
		}
	})
}

// assertSameChunkIDs checks got and want contain the same chunk IDs, regardless
// of order.
func assertSameChunkIDs(t *testing.T, got, want []domain.ChunkID) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("chunk IDs: got %d %v, want %d %v", len(got), got, len(want), want)
		return
	}
	set := make(map[domain.ChunkID]bool, len(want))
	for _, id := range want {
		set[id] = true
	}
	for _, id := range got {
		if !set[id] {
			t.Errorf("unexpected chunk ID %s in %v", id, got)
		}
	}
}
