package domain_test

import (
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func mustSheet(t *testing.T, size, overlap int) domain.SheetChunker {
	t.Helper()
	c, err := domain.NewSheetChunker(size, overlap, wordCount)
	if err != nil {
		t.Fatalf("NewSheetChunker(%d,%d): %v", size, overlap, err)
	}
	return c
}

func chunkSheet(t *testing.T, c domain.SheetChunker, text string) []domain.ChunkResult {
	t.Helper()
	rs, err := c.Chunk(domain.ParsedDoc{Text: text, ContentType: "text/csv"})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	return rs
}

func TestNewSheetChunker(t *testing.T) {
	if _, err := domain.NewSheetChunker(0, 0, wordCount); err == nil {
		t.Error("want error for non-positive size")
	}
	if _, err := domain.NewSheetChunker(10, 10, wordCount); err == nil {
		t.Error("want error for overlap >= size")
	}
	if _, err := domain.NewSheetChunker(10, 2, nil); err == nil {
		t.Error("want error for nil token counter")
	}
}

func TestSheetChunkerRepeatsHeaderOnEveryChunk(t *testing.T) {
	// 3 words of header, 3 per row; size 10 fits header + 2 rows per chunk.
	text := "ID Status Owner\n" +
		"W-1 Open Ruiz\n" +
		"W-2 Open Chen\n" +
		"W-3 Closed Ruiz\n" +
		"W-4 Open Okafor\n"

	got := chunkSheet(t, mustSheet(t, 10, 0), text)
	if len(got) < 2 {
		t.Fatalf("want the rows split across multiple chunks, got %d", len(got))
	}
	for i, r := range got {
		if !strings.HasPrefix(r.Text, "ID Status Owner\n") {
			t.Errorf("chunk %d does not lead with the header row:\n%s", i, r.Text)
		}
		if wordCount(r.Text) > 10 {
			t.Errorf("chunk %d is %d tokens, over the size of 10:\n%s", i, wordCount(r.Text), r.Text)
		}
	}

	// Every data row survives exactly once across the chunks.
	for _, row := range []string{"W-1 Open Ruiz", "W-2 Open Chen", "W-3 Closed Ruiz", "W-4 Open Okafor"} {
		n := 0
		for _, r := range got {
			n += strings.Count(r.Text, row)
		}
		if n != 1 {
			t.Errorf("row %q appears %d times across chunks, want 1", row, n)
		}
	}
}

func TestSheetChunkerCarriesSheetName(t *testing.T) {
	text := domain.SheetHeadingPrefix + "Orders\n" +
		"ID Status\n" +
		"W-1 Open\n" +
		"W-2 Closed\n"

	got := chunkSheet(t, mustSheet(t, 8, 0), text)
	if len(got) == 0 {
		t.Fatal("want at least one chunk")
	}
	for i, r := range got {
		if r.HeadingPath != "Orders" {
			t.Errorf("chunk %d HeadingPath = %q, want %q", i, r.HeadingPath, "Orders")
		}
		if !strings.HasPrefix(r.Text, domain.SheetHeadingPrefix+"Orders\n") {
			t.Errorf("chunk %d does not carry the sheet name:\n%s", i, r.Text)
		}
	}
}

func TestSheetChunkerSplitsSheets(t *testing.T) {
	text := domain.SheetHeadingPrefix + "Orders\n" +
		"ID Status\nW-1 Open\n\n" +
		domain.SheetHeadingPrefix + "Inventory\n" +
		"Host Role\nweb01 frontend\n"

	got := chunkSheet(t, mustSheet(t, 100, 0), text)
	if len(got) != 2 {
		t.Fatalf("want one chunk per sheet, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "W-1 Open") || strings.Contains(got[0].Text, "web01") {
		t.Errorf("sheets bled together in chunk 0:\n%s", got[0].Text)
	}
	if got[1].HeadingPath != "Inventory" {
		t.Errorf("chunk 1 HeadingPath = %q, want %q", got[1].HeadingPath, "Inventory")
	}
	if !strings.Contains(got[1].Text, "Host Role") {
		t.Errorf("chunk 1 lost its header:\n%s", got[1].Text)
	}
}

func TestSheetChunkerUnnamedTable(t *testing.T) {
	// A csv has no sheet marker: header repetition still applies, no heading path.
	// wordCount is whitespace-based, so each comma-joined row is a single token:
	// size 2 leaves a budget of 1 row per chunk once the header is paid for.
	text := "id,status\n1,open\n2,closed\n3,open\n"

	got := chunkSheet(t, mustSheet(t, 2, 0), text)
	if len(got) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(got))
	}
	for i, r := range got {
		if r.HeadingPath != "" {
			t.Errorf("chunk %d HeadingPath = %q, want empty for an unnamed table", i, r.HeadingPath)
		}
		if !strings.HasPrefix(r.Text, "id,status\n") {
			t.Errorf("chunk %d lost the header:\n%s", i, r.Text)
		}
	}
}

func TestSheetChunkerEdgeCases(t *testing.T) {
	c := mustSheet(t, 10, 0)

	t.Run("empty input yields no chunks", func(t *testing.T) {
		for _, in := range []string{"", "   ", "\n\n\t\n"} {
			if got := chunkSheet(t, c, in); len(got) != 0 {
				t.Errorf("Chunk(%q) = %d chunks, want 0", in, len(got))
			}
		}
	})

	t.Run("a header with no data rows is still a chunk", func(t *testing.T) {
		got := chunkSheet(t, c, "ID Status Owner\n")
		if len(got) != 1 || !strings.Contains(got[0].Text, "ID Status Owner") {
			t.Errorf("got %d chunks: %+v", len(got), got)
		}
	})

	t.Run("a sheet with a name but no rows yields nothing", func(t *testing.T) {
		if got := chunkSheet(t, c, domain.SheetHeadingPrefix+"Empty\n"); len(got) != 0 {
			t.Errorf("got %d chunks, want 0", len(got))
		}
	})

	t.Run("a header wider than the chunk size degrades to plain packing", func(t *testing.T) {
		// Header is 5 tokens against a size of 3: there is no room to repeat it,
		// so the rows must still be emitted rather than crowded out.
		got := chunkSheet(t, mustSheet(t, 3, 0), "H1 H2 H3 H4 H5\nr1 r2 r3\nr4 r5 r6\n")
		if len(got) == 0 {
			t.Fatal("rows were dropped when the header did not fit")
		}
		var all strings.Builder
		for _, r := range got {
			all.WriteString(r.Text)
			all.WriteString("\n")
		}
		for _, cell := range []string{"r1", "r3", "r4", "r6"} {
			if !strings.Contains(all.String(), cell) {
				t.Errorf("lost %q:\n%s", cell, all.String())
			}
		}
	})

	t.Run("an oversized row still makes progress and is not dropped", func(t *testing.T) {
		wide := strings.Repeat("cell ", 40)
		got := chunkSheet(t, mustSheet(t, 10, 0), "H1 H2\n"+wide+"\n")
		if len(got) == 0 {
			t.Fatal("oversized row was dropped")
		}
		total := 0
		for _, r := range got {
			total += strings.Count(r.Text, "cell")
		}
		if total < 40 {
			t.Errorf("lost content: %d of 40 cells survived", total)
		}
	})
}
