package domain_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

// wordCount is a deterministic stand-in tokenizer for the chunker tests: one
// "token" per whitespace word, so sizes are easy to reason about without
// depending on the real BPE tokenizer.
func wordCount(s string) int { return len(strings.Fields(s)) }

func mustMarkdown(t *testing.T, size, overlap int) domain.MarkdownChunker {
	t.Helper()
	c, err := domain.NewMarkdownChunker(size, overlap, false, wordCount)
	if err != nil {
		t.Fatalf("NewMarkdownChunker(%d,%d): %v", size, overlap, err)
	}
	return c
}

func chunkMarkdown(t *testing.T, c domain.MarkdownChunker, text string) []domain.ChunkResult {
	t.Helper()
	rs, err := c.Chunk(domain.ParsedDoc{Text: text, ContentType: "text/markdown"})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	return rs
}

func TestNewMarkdownChunker(t *testing.T) {
	if _, err := domain.NewMarkdownChunker(0, 0, false, wordCount); err == nil {
		t.Error("want error for non-positive size")
	}
	if _, err := domain.NewMarkdownChunker(10, 10, false, wordCount); err == nil {
		t.Error("want error for overlap >= size")
	}
	if _, err := domain.NewMarkdownChunker(10, 2, false, nil); err == nil {
		t.Error("want error for nil token counter")
	}
}

func TestMarkdownChunkerContextPrefix(t *testing.T) {
	doc := "# Auth\none two three four five six\n## Keys\nseven eight nine ten eleven twelve\n"

	t.Run("on: embeds heading-path prefix, stores original", func(t *testing.T) {
		c, err := domain.NewMarkdownChunker(10, 0, true, wordCount)
		if err != nil {
			t.Fatal(err)
		}
		got := chunkMarkdown(t, c, doc)
		if len(got) != 2 {
			t.Fatalf("want 2 chunks, got %d", len(got))
		}
		for _, r := range got {
			// Stored text must remain the original (no heading-path prefix), so
			// citations and inspection show real content.
			if strings.HasPrefix(r.Text, r.HeadingPath) && r.HeadingPath != "" {
				t.Errorf("stored Text must not carry the prefix: %q", r.Text)
			}
			// Embedded text must begin with the heading path.
			if !strings.HasPrefix(r.TextToEmbed(), r.HeadingPath+"\n\n") {
				t.Errorf("embedded text should be prefixed with the heading path %q: %q", r.HeadingPath, r.TextToEmbed())
			}
		}
	})

	t.Run("off: embedded text equals stored text", func(t *testing.T) {
		c, err := domain.NewMarkdownChunker(10, 0, false, wordCount)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range chunkMarkdown(t, c, doc) {
			if r.TextToEmbed() != r.Text {
				t.Errorf("with prefix off, embedded text must equal stored text: %q vs %q", r.TextToEmbed(), r.Text)
			}
		}
	})
}

func TestMarkdownChunkerHeadingPath(t *testing.T) {
	// Sized so each ~8-token section stands alone (not merged, not split).
	c := mustMarkdown(t, 10, 0)
	doc := "# Auth\none two three four five six\n## Keys\nseven eight nine ten eleven twelve\n"
	got := chunkMarkdown(t, c, doc)

	if len(got) != 2 {
		t.Fatalf("want 2 chunks, got %d: %+v", len(got), got)
	}
	if got[0].HeadingPath != "Auth" {
		t.Errorf("chunk 0 heading path = %q, want %q", got[0].HeadingPath, "Auth")
	}
	if got[1].HeadingPath != "Auth > Keys" {
		t.Errorf("chunk 1 heading path = %q, want %q", got[1].HeadingPath, "Auth > Keys")
	}
	if !strings.Contains(got[0].Text, "# Auth") || !strings.Contains(got[1].Text, "## Keys") {
		t.Errorf("chunks should retain their heading lines: %+v", got)
	}
}

func TestMarkdownChunkerMergesUndersizedSections(t *testing.T) {
	// Large size: both small sections merge into one chunk under their common
	// heading-path prefix.
	c := mustMarkdown(t, 100, 0)
	doc := "# Auth\none two three four five six\n## Keys\nseven eight nine ten eleven twelve\n"
	got := chunkMarkdown(t, c, doc)

	if len(got) != 1 {
		t.Fatalf("want 1 merged chunk, got %d: %+v", len(got), got)
	}
	if got[0].HeadingPath != "Auth" {
		t.Errorf("merged chunk path = %q, want common prefix %q", got[0].HeadingPath, "Auth")
	}
	if !strings.Contains(got[0].Text, "# Auth") || !strings.Contains(got[0].Text, "## Keys") {
		t.Errorf("merged chunk should contain both sections: %q", got[0].Text)
	}
}

func TestMarkdownChunkerNeverSplitsInsideCodeFence(t *testing.T) {
	c := mustMarkdown(t, 100, 0)
	doc := "# Code\n```\n## not a heading\nline one\n\nline two\n```\ndone\n"
	got := chunkMarkdown(t, c, doc)

	if len(got) != 1 {
		t.Fatalf("want 1 chunk (fence content is opaque), got %d: %+v", len(got), got)
	}
	if got[0].HeadingPath != "Code" {
		t.Errorf("path = %q, want %q", got[0].HeadingPath, "Code")
	}
	if !strings.Contains(got[0].Text, "## not a heading") {
		t.Errorf("fenced ## line must survive verbatim in the chunk: %q", got[0].Text)
	}
	for _, r := range got {
		if r.HeadingPath == "Code > not a heading" || strings.Contains(r.HeadingPath, "not a heading") {
			t.Errorf("a heading inside a code fence was treated as a real heading: %q", r.HeadingPath)
		}
	}
}

func TestMarkdownChunkerSplitsOversizedSectionAtParagraphs(t *testing.T) {
	c := mustMarkdown(t, 12, 0)
	paras := []string{
		"para one has several words here now.",
		"para two also has several words today.",
		"para three is the final paragraph here.",
	}
	doc := "# Big\n" + strings.Join(paras, "\n\n") + "\n"
	got := chunkMarkdown(t, c, doc)

	if len(got) < 2 {
		t.Fatalf("oversized section should split into >1 chunk, got %d", len(got))
	}
	// Every chunk must be composed of whole original paragraphs — never a
	// paragraph cut mid-sentence.
	for _, r := range got {
		if r.HeadingPath != "Big" {
			t.Errorf("sub-chunk lost its heading path: %q", r.HeadingPath)
		}
		for _, frag := range strings.Split(r.Text, "\n\n") {
			frag = strings.TrimSpace(frag)
			if frag == "" || strings.HasPrefix(frag, "# Big") {
				continue
			}
			if !slices.Contains(paras, frag) {
				t.Errorf("chunk fragment is not a whole paragraph (split mid-paragraph): %q", frag)
			}
		}
	}
}

func TestMarkdownChunkerPreamble(t *testing.T) {
	c := mustMarkdown(t, 10, 0)
	doc := "intro paragraph with no heading above it here\n\n# First\nalpha beta gamma delta\n"
	got := chunkMarkdown(t, c, doc)

	if len(got) != 2 {
		t.Fatalf("want 2 chunks (preamble + section), got %d: %+v", len(got), got)
	}
	if got[0].HeadingPath != "" {
		t.Errorf("preamble chunk should have an empty heading path, got %q", got[0].HeadingPath)
	}
	if got[1].HeadingPath != "First" {
		t.Errorf("section chunk path = %q, want %q", got[1].HeadingPath, "First")
	}
}

func TestMarkdownChunkerDeterministic(t *testing.T) {
	c := mustMarkdown(t, 12, 2)
	doc := "# A\n" + words(40) + "\n\n## B\n" + words(40) + "\n### C\n" + words(5) + "\n"
	first := chunkMarkdown(t, c, doc)
	second := chunkMarkdown(t, c, doc)
	if len(first) != len(second) {
		t.Fatalf("non-deterministic chunk count: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("chunk %d differs between runs:\n %+v\n %+v", i, first[i], second[i])
		}
	}
}

func TestMarkdownChunkerEmpty(t *testing.T) {
	c := mustMarkdown(t, 10, 0)
	if got := chunkMarkdown(t, c, "   \n\n  "); len(got) != 0 {
		t.Errorf("whitespace-only input should yield no chunks, got %+v", got)
	}
}
