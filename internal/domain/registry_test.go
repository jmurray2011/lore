package domain_test

import (
	"slices"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

// recordingChunker is a stand-in strategy that tags its single chunk so the
// registry's dispatch can be observed.
type recordingChunker struct{ tag string }

func (c recordingChunker) Chunk(doc domain.ParsedDoc) ([]domain.ChunkResult, error) {
	return []domain.ChunkResult{{Text: c.tag + ":" + doc.Text}}, nil
}

func TestFixedChunkerCharacterization(t *testing.T) {
	// The fixed chunker's Chunk output must reproduce the legacy Split output
	// exactly — same boundaries, same text, in order — so re-chunking under the
	// `fixed` strategy is byte-for-byte the pre-registry behavior.
	fc, err := domain.NewFixedChunker(4, 1)
	if err != nil {
		t.Fatalf("NewFixedChunker: %v", err)
	}
	for _, in := range []string{"", "alpha beta gamma", words(10), words(13), words(100)} {
		want := fc.Split(in)
		results, err := fc.Chunk(domain.ParsedDoc{Text: in, ContentType: "text/plain"})
		if err != nil {
			t.Fatalf("Chunk(%q): %v", in, err)
		}
		got := make([]string, len(results))
		for i, r := range results {
			got[i] = r.Text
		}
		if !slices.Equal(got, want) {
			t.Errorf("Chunk vs Split mismatch for %q:\n got %v\nwant %v", in, got, want)
		}
	}
}

func TestRegistry(t *testing.T) {
	def := recordingChunker{tag: "default"}
	md := recordingChunker{tag: "markdown"}

	t.Run("requires a default", func(t *testing.T) {
		if _, err := domain.NewRegistry(nil, nil); err == nil {
			t.Fatal("want error for nil default")
		}
	})

	t.Run("dispatches by content type, falls back to default", func(t *testing.T) {
		reg, err := domain.NewRegistry(def, map[string]domain.Chunker{"text/markdown": md})
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		cases := []struct {
			contentType string
			wantTag     string
		}{
			{"text/markdown", "markdown"},
			{"text/markdown; charset=utf-8", "markdown"}, // parameters ignored
			{"TEXT/MARKDOWN", "markdown"},                // case-insensitive
			{"text/plain", "default"},                    // unregistered → default
			{"", "default"},
		}
		for _, tc := range cases {
			got, err := reg.Chunk(domain.ParsedDoc{Text: "x", ContentType: tc.contentType})
			if err != nil {
				t.Fatalf("Chunk(%q): %v", tc.contentType, err)
			}
			if len(got) != 1 || got[0].Text != tc.wantTag+":x" {
				t.Errorf("content type %q routed to %v, want tag %q", tc.contentType, got, tc.wantTag)
			}
		}
	})
}
