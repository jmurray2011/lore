package docx

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/limitio"
)

// zipWith builds a zip whose single named entry holds body — enough to drive
// the decompression-cap path without depending on the external test helpers.
func zipWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExtractRejectsOversizedDocumentXML guards against a decompression bomb:
// a small archive whose document.xml expands past the cap must be refused, not
// streamed into memory.
func TestExtractRejectsOversizedDocumentXML(t *testing.T) {
	orig := maxDocumentBytes
	maxDocumentBytes = 64
	defer func() { maxDocumentBytes = orig }()

	body := "<w:document><w:body><w:p><w:r><w:t>" +
		strings.Repeat("A", 200) + "</w:t></w:r></w:p></w:body></w:document>"
	raw := zipWith(t, "word/document.xml", body)

	_, err := New().Extract(ContentType, raw)
	if !errors.Is(err, limitio.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge for oversized document.xml, got %v", err)
	}
}
