package docx_test

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/docx"
)

// makeDocx builds the minimal docx the extractor reads: a zip containing
// word/document.xml. The other OOXML parts are irrelevant to text extraction.
func makeDocx(t *testing.T, documentXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, documentXML); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const sampleDocument = `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>
<w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t> World</w:t></w:r></w:p>
<w:p><w:r><w:t>Second paragraph</w:t></w:r></w:p>
</w:body>
</w:document>`

func TestSupports(t *testing.T) {
	e := docx.New()
	if !e.Supports(docx.ContentType) {
		t.Errorf("want Supports(%q) = true", docx.ContentType)
	}
	for _, ct := range []string{"text/plain", "application/pdf", "image/png", ""} {
		if e.Supports(ct) {
			t.Errorf("want Supports(%q) = false", ct)
		}
	}
}

func TestExtract(t *testing.T) {
	e := docx.New()

	t.Run("concatenates runs, paragraphs become newlines", func(t *testing.T) {
		got, err := e.Extract(docx.ContentType, makeDocx(t, sampleDocument))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		want := "Hello World\nSecond paragraph"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("unsupported content type is an error", func(t *testing.T) {
		if _, err := e.Extract("text/plain", makeDocx(t, sampleDocument)); err == nil {
			t.Error("want error for unsupported type")
		}
	})

	t.Run("non-zip bytes are an error", func(t *testing.T) {
		if _, err := e.Extract(docx.ContentType, []byte("not a zip")); err == nil {
			t.Error("want error for non-zip content")
		}
	})

	t.Run("missing word/document.xml is an error", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if _, err := zw.Create("word/other.xml"); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Extract(docx.ContentType, buf.Bytes()); err == nil {
			t.Error("want error when document.xml is absent")
		}
	})
}
