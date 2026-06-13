package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestChunkIDValid(t *testing.T) {
	good := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///a.md"), 5)
	if !good.Valid() {
		t.Errorf("derived chunk ID %q should be valid", good)
	}
	hash := strings.Repeat("a", 64)
	bad := []domain.ChunkID{
		"",                           // empty
		domain.ChunkID(hash),         // no seq
		domain.ChunkID(hash + ":"),   // empty seq
		domain.ChunkID(hash + ":x"),  // non-numeric seq
		domain.ChunkID("deadbeef:0"), // hash too short
		domain.ChunkID(strings.Repeat("g", 64) + ":0"), // non-hex
		domain.ChunkID(strings.Repeat("A", 64) + ":0"), // uppercase (non-canonical)
		"3f2a9c", // not even shaped like one
	}
	for _, id := range bad {
		if id.Valid() {
			t.Errorf("%q should be invalid", id)
		}
	}
}

func TestNewChunk(t *testing.T) {
	docID := domain.DeriveDocumentID("docs", "file:///a.md")

	t.Run("valid", func(t *testing.T) {
		c, err := domain.NewChunk(docID, 0, "some text")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.ID == "" || c.DocumentID != docID || c.Seq != 0 || c.Text != "some text" {
			t.Errorf("got %+v", c)
		}
	})

	t.Run("deterministic ID", func(t *testing.T) {
		a, _ := domain.NewChunk(docID, 3, "text")
		b, _ := domain.NewChunk(docID, 3, "text")
		if a.ID != b.ID {
			t.Error("same (doc, seq) must derive same chunk ID")
		}
		c, _ := domain.NewChunk(docID, 4, "text")
		if a.ID == c.ID {
			t.Error("different seq must derive different chunk ID")
		}
	})

	t.Run("rejects invalid", func(t *testing.T) {
		if _, err := domain.NewChunk("", 0, "t"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("empty doc ID: want ErrInvalidArgument, got %v", err)
		}
		if _, err := domain.NewChunk(docID, -1, "t"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("negative seq: want ErrInvalidArgument, got %v", err)
		}
		if _, err := domain.NewChunk(docID, 0, ""); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("empty text: want ErrInvalidArgument, got %v", err)
		}
	})
}
