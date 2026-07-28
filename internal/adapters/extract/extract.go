// Package extract turns supported raw content into plain text for chunking. It
// handles text/plain, text/markdown, and text/csv; markdown is currently
// embedded as-is (heading-aware handling is a later refinement), and csv is
// passed through verbatim so the sheet chunker can treat its first line as a
// header row. Newlines are normalized to "\n" so a file's content hash is stable
// across platforms.
package extract

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jmurray2011/lore/internal/app"
)

// Extractor extracts text from supported content types. Its zero value is ready.
type Extractor struct{}

// compile-time port check
var _ app.Extractor = Extractor{}

// New returns a text Extractor.
func New() Extractor { return Extractor{} }

// Supports reports whether the content type can be extracted.
func (Extractor) Supports(contentType string) bool { return supported(contentType) }

// Extract returns the UTF-8 text of raw, with newlines normalized to "\n".
func (Extractor) Extract(contentType string, raw []byte) (string, error) {
	if !supported(contentType) {
		return "", fmt.Errorf("extract: unsupported content type %q", contentType)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("extract: %q content is not valid UTF-8", contentType)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n"), nil
}

func supported(contentType string) bool {
	switch baseType(contentType) {
	case "text/plain", "text/markdown", "text/csv":
		return true
	default:
		return false
	}
}

// baseType drops any "; charset=..." parameters and lower-cases the type.
func baseType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.TrimSpace(strings.ToLower(contentType))
}
