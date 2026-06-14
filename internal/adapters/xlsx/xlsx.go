// Package xlsx is an Extractor for Excel .xlsx files. A .xlsx is a zip of OOXML
// SpreadsheetML parts; most cell text lives in a shared-string table
// (xl/sharedStrings.xml) referenced by cells in xl/worksheets/*.xml. This
// extractor resolves those references and emits one line per row (cells
// tab-joined) so row context — e.g. a control ID beside its status — survives
// for retrieval. Formatting, formulas, and merged-cell geometry are not
// preserved. Stdlib only.
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/limitio"
)

// ContentType is the OOXML SpreadsheetML media type for .xlsx files.
const ContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// An .xlsx is untrusted input that may come from a third party. A small archive
// can expand to gigabytes (a zip bomb), so decompression is bounded twice: each
// OOXML part individually, and the running total of emitted text. Both are vars,
// not consts, only so tests can lower them.
var (
	// maxPartBytes caps the decompressed size of any single OOXML part.
	maxPartBytes int64 = 256 << 20
	// maxWorkbookBytes caps the total text accumulated across all sheets, so
	// many parts each under maxPartBytes cannot sum without bound.
	maxWorkbookBytes int64 = 512 << 20
)

// Extractor extracts plain text from .xlsx content. Its zero value is ready.
type Extractor struct{}

// compile-time port check
var _ app.Extractor = Extractor{}

// New returns an xlsx Extractor.
func New() Extractor { return Extractor{} }

// Supports reports whether the content type is an .xlsx workbook.
func (Extractor) Supports(contentType string) bool { return baseType(contentType) == ContentType }

// Extract returns the workbook's text: each sheet's rows, one row per line with
// cells tab-separated, sheets separated by a blank line.
func (e Extractor) Extract(contentType string, raw []byte) (string, error) {
	if !e.Supports(contentType) {
		return "", fmt.Errorf("xlsx: unsupported content type %q", contentType)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("xlsx: open archive: %w", err)
	}

	shared, err := readSharedStrings(zr)
	if err != nil {
		return "", err
	}

	var sheets []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f.Name)
		}
	}
	sort.Strings(sheets)

	var b strings.Builder
	for _, name := range sheets {
		text, err := readSheet(zr, name, shared)
		if err != nil {
			return "", err
		}
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		if int64(b.Len()) > maxWorkbookBytes {
			return "", fmt.Errorf("xlsx: workbook text: %w", limitio.ErrTooLarge)
		}
	}
	return b.String(), nil
}

type stringItem struct {
	T string `xml:"t"`
	R []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (s stringItem) text() string {
	if len(s.R) == 0 {
		return s.T
	}
	var b strings.Builder
	for _, run := range s.R {
		b.WriteString(run.T)
	}
	return b.String()
}

func readSharedStrings(zr *zip.Reader) ([]string, error) {
	data, ok, err := readFile(zr, "xl/sharedStrings.xml")
	if err != nil || !ok {
		return nil, err // absent table is fine: cells may be inline/numeric
	}
	var doc struct {
		Items []stringItem `xml:"si"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("xlsx: parse sharedStrings: %w", err)
	}
	out := make([]string, len(doc.Items))
	for i, si := range doc.Items {
		out[i] = si.text()
	}
	return out, nil
}

func readSheet(zr *zip.Reader, name string, shared []string) (string, error) {
	data, _, err := readFile(zr, name)
	if err != nil {
		return "", err
	}
	var doc struct {
		Rows []struct {
			Cells []struct {
				Type string `xml:"t,attr"`
				V    string `xml:"v"`
				Is   struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("xlsx: parse %s: %w", name, err)
	}

	var b strings.Builder
	for _, row := range doc.Rows {
		cells := make([]string, 0, len(row.Cells))
		for _, c := range row.Cells {
			var v string
			switch c.Type {
			case "s":
				if i, err := strconv.Atoi(strings.TrimSpace(c.V)); err == nil && i >= 0 && i < len(shared) {
					v = shared[i]
				}
			case "inlineStr":
				v = c.Is.T
			default:
				v = c.V
			}
			if v != "" {
				cells = append(cells, v)
			}
		}
		if len(cells) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Join(cells, "\t"))
	}
	return b.String(), nil
}

func readFile(zr *zip.Reader, name string) ([]byte, bool, error) {
	for _, f := range zr.File {
		if f.Name == name {
			// Fast-fail on an honestly-declared bomb before decompressing; the
			// streaming limit below is the hard bound for a lying header.
			if f.UncompressedSize64 > uint64(maxPartBytes) {
				return nil, false, fmt.Errorf("xlsx: %s: %w", name, limitio.ErrTooLarge)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, false, fmt.Errorf("xlsx: open %s: %w", name, err)
			}
			defer func() { _ = rc.Close() }()
			data, err := limitio.ReadAll(rc, maxPartBytes)
			if err != nil {
				return nil, false, fmt.Errorf("xlsx: read %s: %w", name, err)
			}
			return data, true, nil
		}
	}
	return nil, false, nil
}

// baseType drops any "; charset=..." parameters and lower-cases the type.
func baseType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.TrimSpace(strings.ToLower(contentType))
}
