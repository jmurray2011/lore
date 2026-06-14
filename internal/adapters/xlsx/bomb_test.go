package xlsx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/limitio"
)

// zipParts builds a zip with the given name→body entries, in insertion order.
func zipParts(t *testing.T, parts ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, p[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sheetXML wraps cells into a single-row worksheet of inline strings.
func sheetXML(text string) string {
	return `<worksheet><sheetData><row><c t="inlineStr"><is><t>` +
		text + `</t></is></c></row></sheetData></worksheet>`
}

// TestExtractRejectsOversizedPart guards against a decompression bomb in a
// single OOXML part.
func TestExtractRejectsOversizedPart(t *testing.T) {
	orig := maxPartBytes
	maxPartBytes = 128
	defer func() { maxPartBytes = orig }()

	raw := zipParts(t, [2]string{"xl/worksheets/sheet1.xml", sheetXML(strings.Repeat("A", 500))})

	_, err := New().Extract(ContentType, raw)
	if !errors.Is(err, limitio.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge for oversized part, got %v", err)
	}
}

// TestExtractRejectsOversizedWorkbook guards the aggregate: many parts each
// under the per-part cap can still sum past the workbook cap.
func TestExtractRejectsOversizedWorkbook(t *testing.T) {
	origWB := maxWorkbookBytes
	maxWorkbookBytes = 256
	defer func() { maxWorkbookBytes = origWB }()

	var parts [][2]string
	for i := 0; i < 10; i++ {
		parts = append(parts, [2]string{
			fmt.Sprintf("xl/worksheets/sheet%d.xml", i),
			sheetXML(strings.Repeat("B", 100)),
		})
	}

	_, err := New().Extract(ContentType, zipParts(t, parts...))
	if !errors.Is(err, limitio.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge for oversized workbook, got %v", err)
	}
}
