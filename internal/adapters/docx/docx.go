// Package docx is an Extractor for Word .docx files. A .docx is a zip of OOXML
// parts; the document text lives in word/document.xml as <w:t> runs grouped in
// <w:p> paragraphs. This extractor reads only that part, concatenating run text
// and turning paragraph boundaries into newlines — no dependency beyond stdlib.
// Formatting, tables, headers/footers, and footnotes are not preserved.
package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/limitio"
)

// ContentType is the OOXML WordprocessingML media type for .docx files.
const ContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// maxDocumentBytes caps the decompressed size of word/document.xml. A .docx is
// untrusted input that may come from a third party, and a small archive can
// expand to gigabytes (a zip bomb); this bounds the work an extractor will do
// before giving up. It is a var, not a const, only so tests can lower it.
var maxDocumentBytes int64 = 256 << 20

// Extractor extracts plain text from .docx content. Its zero value is ready.
type Extractor struct{}

// compile-time port check
var _ app.Extractor = Extractor{}

// New returns a docx Extractor.
func New() Extractor { return Extractor{} }

// Supports reports whether the content type is a .docx document.
func (Extractor) Supports(contentType string) bool { return baseType(contentType) == ContentType }

// Extract returns the document's text, with paragraphs separated by "\n".
func (e Extractor) Extract(contentType string, raw []byte) (string, error) {
	if !e.Supports(contentType) {
		return "", fmt.Errorf("docx: unsupported content type %q", contentType)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("docx: open archive: %w", err)
	}
	doc, err := openDocumentXML(zr)
	if err != nil {
		return "", err
	}
	defer func() { _ = doc.Close() }()
	return parseDocument(limitio.Reader(doc, maxDocumentBytes))
}

func openDocumentXML(zr *zip.Reader) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			// Fast-fail on an honestly-declared bomb before decompressing; the
			// streaming limit in Extract is the hard bound for a lying header.
			if f.UncompressedSize64 > uint64(maxDocumentBytes) {
				return nil, fmt.Errorf("docx: document.xml: %w", limitio.ErrTooLarge)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("docx: open document.xml: %w", err)
			}
			return rc, nil
		}
	}
	return nil, fmt.Errorf("docx: archive has no word/document.xml")
}

// parseDocument streams the WordprocessingML body, appending <w:t> text and a
// newline at the end of each <w:p> paragraph.
func parseDocument(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("docx: parse document.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				var text string
				if err := dec.DecodeElement(&text, &t); err != nil {
					return "", fmt.Errorf("docx: read text run: %w", err)
				}
				b.WriteString(text)
			}
		case xml.EndElement:
			if t.Name.Local == "p" {
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// baseType drops any "; charset=..." parameters and lower-cases the type.
func baseType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.TrimSpace(strings.ToLower(contentType))
}
