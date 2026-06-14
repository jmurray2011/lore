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
// The within-port cascade (deleting a Document deletes its Chunks)
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
			ch.HeadingPath = fmt.Sprintf("%s > section %d", content, i)
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

	t.Run("upsert preserves the document fingerprint", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		doc.Fingerprint = "1234:deadbeef"
		mustUpsert(t, repo, doc, chunks)

		got, err := repo.GetBySource(ctx, "docs", "file:///a.md")
		if err != nil {
			t.Fatalf("GetBySource: %v", err)
		}
		if got.Fingerprint != "1234:deadbeef" {
			t.Errorf("fingerprint not persisted: got %q, want %q", got.Fingerprint, "1234:deadbeef")
		}
	})

	t.Run("upsert round-trips document metadata as an independent copy", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		doc.Metadata = domain.Metadata{"author": "alice", "tags": "security,compliance"}
		mustUpsert(t, repo, doc, chunks)

		// Mutating the caller's map after Upsert must not reach the store.
		doc.Metadata["author"] = "eve"

		got, err := repo.GetBySource(ctx, "docs", "file:///a.md")
		if err != nil {
			t.Fatalf("GetBySource: %v", err)
		}
		if got.Metadata["author"] != "alice" || got.Metadata["tags"] != "security,compliance" {
			t.Fatalf("metadata not persisted independently: got %v", got.Metadata)
		}

		// Mutating the returned map must not corrupt the store either.
		got.Metadata["author"] = "mallory"
		again, err := repo.GetDocuments(ctx, []domain.DocumentID{doc.ID})
		if err != nil || len(again) != 1 {
			t.Fatalf("GetDocuments: %v, %v", again, err)
		}
		if again[0].Metadata["author"] != "alice" {
			t.Errorf("repository must return an independent metadata copy; got %v", again[0].Metadata)
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

	t.Run("get chunks by document returns them in seq order, isolated by document", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 3)
		docB, chunksB := newDoc(t, "docs", "file:///b.md", "beta", 2)
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, docB, chunksB)

		got, err := repo.GetChunksByDocument(ctx, "docs", docA.ID)
		if err != nil {
			t.Fatalf("GetChunksByDocument: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d chunks, want 3", len(got))
		}
		for i, ch := range got {
			if ch.Seq != i {
				t.Errorf("chunk at index %d has seq %d, want %d (must be seq order)", i, ch.Seq, i)
			}
			if ch.DocumentID != docA.ID {
				t.Errorf("a chunk from another document leaked: %v", ch.DocumentID)
			}
			if ch.HeadingPath != chunksA[i].HeadingPath {
				t.Errorf("chunk %d heading path not persisted: got %q, want %q", i, ch.HeadingPath, chunksA[i].HeadingPath)
			}
		}
	})

	t.Run("get chunks by document is empty for an unknown document, no error", func(t *testing.T) {
		repo := factory(t)
		got, err := repo.GetChunksByDocument(ctx, "docs", domain.DeriveDocumentID("docs", "file:///ghost.md"))
		if err != nil {
			t.Fatalf("GetChunksByDocument: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("want no chunks for an unknown document, got %d", len(got))
		}
	})

	t.Run("get chunks by IDs returns input order, omitting absent and cross-collection IDs", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 3)
		other, chunksOther := newDoc(t, "notes", "file:///b.md", "beta", 1) // a different collection
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, other, chunksOther)

		ids := []string{
			string(chunksA[2].ID),
			"no-such-chunk",
			string(chunksA[0].ID),
			string(chunksOther[0].ID), // belongs to "notes", not "docs"
		}
		got, err := repo.GetChunksByIDs(ctx, "docs", ids)
		if err != nil {
			t.Fatalf("GetChunksByIDs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 chunks (absent + cross-collection omitted), got %d", len(got))
		}
		if got[0].ID != chunksA[2].ID || got[1].ID != chunksA[0].ID {
			t.Errorf("input order not preserved: got [%s %s]", got[0].ID, got[1].ID)
		}
	})

	t.Run("get chunks by IDs is empty for an unknown collection, no error", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		mustUpsert(t, repo, doc, chunks)

		got, err := repo.GetChunksByIDs(ctx, "ghost", []string{string(chunks[0].ID)})
		if err != nil {
			t.Fatalf("GetChunksByIDs: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("want no chunks for an unknown collection, got %d", len(got))
		}
	})

	t.Run("get documents by id preserves input order and skips missing IDs", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		docB, chunksB := newDoc(t, "docs", "file:///b.md", "beta", 1)
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, docB, chunksB)

		missing := domain.DeriveDocumentID("docs", "file:///gone.md")
		got, err := repo.GetDocuments(ctx, []domain.DocumentID{docB.ID, missing, docA.ID})
		if err != nil {
			t.Fatalf("GetDocuments: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 documents (missing skipped), got %d", len(got))
		}
		if got[0].ID != docB.ID || got[1].ID != docA.ID {
			t.Errorf("input order not preserved: got [%s %s]", got[0].ID, got[1].ID)
		}
		if got[0].SourceURI != docB.SourceURI {
			t.Errorf("document not hydrated: got SourceURI %q, want %q", got[0].SourceURI, docB.SourceURI)
		}
	})

	t.Run("list documents returns a collection's documents, isolated by collection", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		docB, chunksB := newDoc(t, "docs", "file:///b.md", "beta", 2)
		other, otherChunks := newDoc(t, "notes", "file:///c.md", "gamma", 1)
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, docB, chunksB)
		mustUpsert(t, repo, other, otherChunks)

		got, err := repo.ListDocuments(ctx, "docs")
		if err != nil {
			t.Fatalf("ListDocuments: %v", err)
		}
		ids := make(map[domain.DocumentID]bool, len(got))
		for _, d := range got {
			ids[d.ID] = true
		}
		if len(got) != 2 || !ids[docA.ID] || !ids[docB.ID] {
			t.Errorf("want documents A and B in docs, got %d: %v", len(got), ids)
		}
		if ids[other.ID] {
			t.Errorf("ListDocuments must not cross collections")
		}
	})

	t.Run("list documents of an empty collection returns nothing, no error", func(t *testing.T) {
		repo := factory(t)
		got, err := repo.ListDocuments(ctx, "empty")
		if err != nil || len(got) != 0 {
			t.Errorf("want empty, nil; got %v, %v", got, err)
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
			t.Errorf("deleting a document must delete its chunks; got %d", len(got))
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

	t.Run("delete chunks removes only the named chunks, keeps the document and its other chunks", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 3)
		mustUpsert(t, repo, doc, chunks)

		removed, err := repo.DeleteChunks(ctx, "docs", []domain.ChunkID{chunks[1].ID})
		if err != nil {
			t.Fatalf("DeleteChunks: %v", err)
		}
		assertSameChunkIDs(t, removed, []domain.ChunkID{chunks[1].ID})

		// The document record survives losing a chunk (this is sub-document
		// redaction, not document deletion).
		if _, err := repo.GetBySource(ctx, "docs", "file:///a.md"); err != nil {
			t.Errorf("document must survive chunk deletion: %v", err)
		}
		got, err := repo.GetChunks(ctx, []domain.ChunkID{chunks[0].ID, chunks[1].ID, chunks[2].ID})
		if err != nil {
			t.Fatalf("GetChunks: %v", err)
		}
		gotIDs := make([]domain.ChunkID, len(got))
		for i, c := range got {
			gotIDs[i] = c.ID
		}
		assertSameChunkIDs(t, gotIDs, []domain.ChunkID{chunks[0].ID, chunks[2].ID})
	})

	t.Run("delete chunks skips IDs absent from the collection, removing none of them", func(t *testing.T) {
		repo := factory(t)
		docA, chunksA := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		docB, chunksB := newDoc(t, "notes", "file:///b.md", "beta", 1)
		mustUpsert(t, repo, docA, chunksA)
		mustUpsert(t, repo, docB, chunksB)

		unknown := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///ghost.md"), 0)
		removed, err := repo.DeleteChunks(ctx, "docs", []domain.ChunkID{chunksB[0].ID, unknown})
		if err != nil {
			t.Fatalf("DeleteChunks: %v", err)
		}
		if len(removed) != 0 {
			t.Errorf("must not remove cross-collection or unknown chunks, removed %v", removed)
		}
		got, err := repo.GetChunks(ctx, []domain.ChunkID{chunksB[0].ID})
		if err != nil {
			t.Fatalf("GetChunks: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("a chunk in another collection must be untouched, got %d", len(got))
		}
	})

	t.Run("delete chunks on an unknown collection removes nothing, no error", func(t *testing.T) {
		repo := factory(t)
		doc, chunks := newDoc(t, "docs", "file:///a.md", "alpha", 1)
		mustUpsert(t, repo, doc, chunks)

		removed, err := repo.DeleteChunks(ctx, "ghost", []domain.ChunkID{chunks[0].ID})
		if err != nil || len(removed) != 0 {
			t.Errorf("unknown collection: want empty/nil, got %v / %v", removed, err)
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
