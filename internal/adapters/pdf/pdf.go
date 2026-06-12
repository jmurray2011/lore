// Package pdf is an Extractor for PDF files, backed by the pure-Go
// github.com/ledongthuc/pdf (decision 18: no cgo, so the static-binary and
// cross-compile goals hold). Extraction is best-effort text in reading order —
// layout, columns, and tables are not preserved, and scanned/image-only PDFs
// yield nothing. The library can panic on malformed input, so Extract recovers
// and reports an error rather than crashing the process.
package pdf

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	pdflib "github.com/ledongthuc/pdf"

	"github.com/jmurray2011/lore/internal/app"
)

// ContentType is the media type for PDF files.
const ContentType = "application/pdf"

// Extractor extracts plain text from PDF content. Its zero value is ready.
type Extractor struct{}

// compile-time port check
var _ app.Extractor = Extractor{}

// New returns a PDF Extractor.
func New() Extractor { return Extractor{} }

// Supports reports whether the content type is a PDF.
func (Extractor) Supports(contentType string) bool { return baseType(contentType) == ContentType }

// Extract returns the PDF's text in reading order. A malformed PDF yields an
// error (including one recovered from a panic in the underlying library), never
// a crash.
func (e Extractor) Extract(contentType string, raw []byte) (text string, err error) {
	if !e.Supports(contentType) {
		return "", fmt.Errorf("pdf: unsupported content type %q", contentType)
	}
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("pdf: malformed document: %v", r)
		}
	}()

	r, err := pdflib.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("pdf: open: %w", err)
	}
	tr, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("pdf: extract text: %w", err)
	}
	var b strings.Builder
	if _, err := io.Copy(&b, tr); err != nil {
		return "", fmt.Errorf("pdf: read text: %w", err)
	}
	return strings.TrimSpace(b.String()), nil
}

// baseType drops any "; charset=..." parameters and lower-cases the type.
func baseType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.TrimSpace(strings.ToLower(contentType))
}
