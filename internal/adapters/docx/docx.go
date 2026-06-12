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
)

// ContentType is the OOXML WordprocessingML media type for .docx files.
const ContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

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
	return parseDocument(doc)
}

func openDocumentXML(zr *zip.Reader) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
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
