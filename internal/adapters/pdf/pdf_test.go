package pdf_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/pdf"
)

// makePDF builds a minimal single-page PDF that shows text via a standard
// Helvetica font, computing correct xref byte offsets so the fixture is valid.
func makePDF(t *testing.T, text string) []byte {
	t.Helper()
	content := "BT /F1 24 Tf 72 720 Td (" + text + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(objs)+1, xrefOff)
	return buf.Bytes()
}

func TestSupports(t *testing.T) {
	e := pdf.New()
	if !e.Supports(pdf.ContentType) {
		t.Errorf("want Supports(%q) = true", pdf.ContentType)
	}
	for _, ct := range []string{"text/plain", "application/octet-stream", "image/png", ""} {
		if e.Supports(ct) {
			t.Errorf("want Supports(%q) = false", ct)
		}
	}
}

func TestExtract(t *testing.T) {
	e := pdf.New()

	t.Run("extracts page text", func(t *testing.T) {
		const want = "Hello World Lore"
		got, err := e.Extract(pdf.ContentType, makePDF(t, want))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("extracted text %q does not contain %q", got, want)
		}
	})

	t.Run("unsupported content type is an error", func(t *testing.T) {
		if _, err := e.Extract("text/plain", makePDF(t, "x")); err == nil {
			t.Error("want error for unsupported type")
		}
	})

	t.Run("non-PDF bytes are an error, not a panic", func(t *testing.T) {
		if _, err := e.Extract(pdf.ContentType, []byte("definitely not a pdf")); err == nil {
			t.Error("want error for non-PDF content")
		}
	})
}
