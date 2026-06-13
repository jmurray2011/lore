package domain_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func mustText(t *testing.T, size, overlap int) domain.TextChunker {
	t.Helper()
	c, err := domain.NewTextChunker(size, overlap, wordCount)
	if err != nil {
		t.Fatalf("NewTextChunker(%d,%d): %v", size, overlap, err)
	}
	return c
}

func chunkText(t *testing.T, c domain.TextChunker, text string) []domain.ChunkResult {
	t.Helper()
	rs, err := c.Chunk(domain.ParsedDoc{Text: text, ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	return rs
}

func TestNewTextChunker(t *testing.T) {
	if _, err := domain.NewTextChunker(0, 0, wordCount); err == nil {
		t.Error("want error for non-positive size")
	}
	if _, err := domain.NewTextChunker(10, 10, wordCount); err == nil {
		t.Error("want error for overlap >= size")
	}
	if _, err := domain.NewTextChunker(10, 2, nil); err == nil {
		t.Error("want error for nil token counter")
	}
}

func TestTextChunkerPacksParagraphs(t *testing.T) {
	paras := []string{
		"para one has five words.",      // 5 words
		"para two also has five words.", // 6 words
		"third paragraph is here now.",  // 5 words
	}
	doc := strings.Join(paras, "\n\n")
	c := mustText(t, 12, 0)
	got := chunkText(t, c, doc)

	if len(got) < 2 {
		t.Fatalf("want the paragraphs split across >1 chunk at size 12, got %d", len(got))
	}
	// No heading path on plain text; every chunk is whole paragraphs.
	for _, r := range got {
		if r.HeadingPath != "" {
			t.Errorf("plain text should have no heading path, got %q", r.HeadingPath)
		}
		for _, frag := range strings.Split(r.Text, "\n\n") {
			if frag = strings.TrimSpace(frag); frag != "" && !slices.Contains(paras, frag) {
				t.Errorf("chunk fragment is not a whole paragraph: %q", frag)
			}
		}
	}
}

func TestTextChunkerSmallDocIsOneChunk(t *testing.T) {
	c := mustText(t, 100, 10)
	got := chunkText(t, c, "a short paragraph\n\nand another short one")
	if len(got) != 1 {
		t.Fatalf("a doc within size should be a single chunk, got %d", len(got))
	}
}

func TestTextChunkerEmpty(t *testing.T) {
	c := mustText(t, 10, 0)
	if got := chunkText(t, c, "   \n\n  "); len(got) != 0 {
		t.Errorf("whitespace-only input should yield no chunks, got %+v", got)
	}
}

func TestTextChunkerDeterministic(t *testing.T) {
	c := mustText(t, 12, 3)
	doc := words(50) + "\n\n" + words(50)
	if !slices.Equal(chunkResultTexts(chunkText(t, c, doc)), chunkResultTexts(chunkText(t, c, doc))) {
		t.Error("TextChunker must be deterministic")
	}
}

func chunkResultTexts(rs []domain.ChunkResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Text
	}
	return out
}
