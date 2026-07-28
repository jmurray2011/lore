package xlsx_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/xlsx"
	"github.com/jmurray2011/lore/internal/domain"
)

const (
	ns    = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	relNS = `http://schemas.openxmlformats.org/officeDocument/2006/relationships`
)

// makeXlsx builds the minimal parts the extractor reads: a shared-string table
// and one worksheet's sheetData.
func makeXlsx(t *testing.T, shared []string, sheetData string) []byte {
	t.Helper()
	var sst strings.Builder
	sst.WriteString(`<?xml version="1.0"?><sst ` + ns + `>`)
	for _, s := range shared {
		sst.WriteString("<si><t>" + s + "</t></si>")
	}
	sst.WriteString("</sst>")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	write("xl/sharedStrings.xml", sst.String())
	write("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet `+ns+`><sheetData>`+sheetData+`</sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sheetSpec is one worksheet in a workbook built by makeWorkbook.
type sheetSpec struct {
	name string // the tab name, as it appears in xl/workbook.xml
	data string // raw <row> elements
}

// xmlAttr renders s as a quoted, XML-escaped attribute value, the way Excel
// writes a tab name containing "&" or "<".
func xmlAttr(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		t.Fatal(err)
	}
	return `"` + b.String() + `"`
}

// makeWorkbook builds an xlsx carrying a real workbook part and relationships,
// so the extractor has to resolve tab names the way Excel writes them.
func makeWorkbook(t *testing.T, shared []string, sheets []sheetSpec, absoluteTargets bool) []byte {
	t.Helper()
	var sst strings.Builder
	sst.WriteString(`<?xml version="1.0"?><sst ` + ns + `>`)
	for _, s := range shared {
		sst.WriteString("<si><t>" + s + "</t></si>")
	}
	sst.WriteString("</sst>")

	var wb, rels strings.Builder
	wb.WriteString(`<?xml version="1.0"?><workbook ` + ns + ` xmlns:r="` + relNS + `"><sheets>`)
	rels.WriteString(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}

	for i, s := range sheets {
		// Deliberately not in tab order on disk: partN is reversed relative to the
		// workbook order, so a part-name sort would produce the wrong sequence.
		part := fmt.Sprintf("worksheets/sheet%d.xml", len(sheets)-i)
		id := fmt.Sprintf("rId%d", i+1)
		fmt.Fprintf(&wb, `<sheet name=%s sheetId="%d" r:id=%q/>`, xmlAttr(t, s.name), i+1, id)
		target := part
		if absoluteTargets {
			target = "/xl/" + part
		}
		fmt.Fprintf(&rels, `<Relationship Id=%q Type="%s/worksheet" Target=%q/>`, id, relNS, target)
		write("xl/"+part, `<?xml version="1.0"?><worksheet `+ns+`><sheetData>`+s.data+`</sheetData></worksheet>`)
	}
	wb.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)

	write("xl/sharedStrings.xml", sst.String())
	write("xl/workbook.xml", wb.String())
	write("xl/_rels/workbook.xml.rels", rels.String())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractSheetNames(t *testing.T) {
	e := xlsx.New()
	shared := []string{"ID", "Status", "W-1", "Open"}
	header := `<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row>`
	body := `<row><c t="s"><v>2</v></c><c t="s"><v>3</v></c></row>`

	t.Run("names each sheet in workbook tab order", func(t *testing.T) {
		got, err := e.Extract(xlsx.ContentType, makeWorkbook(t, shared, []sheetSpec{
			{name: "Orders", data: header + body},
			{name: "Parts & Labor", data: header + body},
		}, false))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		wantFirst := domain.SheetHeadingPrefix + "Orders"
		wantSecond := domain.SheetHeadingPrefix + "Parts & Labor"
		if !strings.Contains(got, wantFirst) || !strings.Contains(got, wantSecond) {
			t.Fatalf("sheet names missing; got:\n%s", got)
		}
		if i, j := strings.Index(got, wantFirst), strings.Index(got, wantSecond); i > j {
			t.Errorf("sheets out of tab order; got:\n%s", got)
		}
	})

	t.Run("resolves absolute relationship targets", func(t *testing.T) {
		got, err := e.Extract(xlsx.ContentType, makeWorkbook(t, shared, []sheetSpec{
			{name: "Inventory", data: header + body},
		}, true))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if !strings.Contains(got, domain.SheetHeadingPrefix+"Inventory") {
			t.Errorf("absolute target not resolved; got:\n%s", got)
		}
		if !strings.Contains(got, "W-1\tOpen") {
			t.Errorf("rows lost; got:\n%s", got)
		}
	})

	t.Run("falls back to the part name when the workbook part is absent", func(t *testing.T) {
		got, err := e.Extract(xlsx.ContentType, makeXlsx(t, shared, header+body))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if !strings.Contains(got, domain.SheetHeadingPrefix+"sheet1") {
			t.Errorf("want fallback sheet name; got:\n%s", got)
		}
	})
}

func TestSupports(t *testing.T) {
	e := xlsx.New()
	if !e.Supports(xlsx.ContentType) {
		t.Errorf("want Supports(%q) = true", xlsx.ContentType)
	}
	for _, ct := range []string{"text/plain", "application/pdf", xlsx.ContentType + "x", ""} {
		if e.Supports(ct) {
			t.Errorf("want Supports(%q) = false", ct)
		}
	}
}

func TestExtract(t *testing.T) {
	e := xlsx.New()

	t.Run("reconstructs rows, resolving shared strings and inline numbers", func(t *testing.T) {
		shared := []string{"Item", "A-2 Widget Assembly", "Status", "Open"}
		// row1: Item | Status   row2: A-2... | Open | 42 (inline number)
		sheet := `<row><c t="s"><v>0</v></c><c t="s"><v>2</v></c></row>` +
			`<row><c t="s"><v>1</v></c><c t="s"><v>3</v></c><c><v>42</v></c></row>`

		got, err := e.Extract(xlsx.ContentType, makeXlsx(t, shared, sheet))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if !strings.Contains(got, "A-2 Widget Assembly\tOpen\t42") {
			t.Errorf("row context lost; got:\n%s", got)
		}
		if !strings.Contains(got, "Item\tStatus") {
			t.Errorf("header row missing; got:\n%s", got)
		}
	})

	t.Run("unsupported content type is an error", func(t *testing.T) {
		if _, err := e.Extract("text/plain", makeXlsx(t, nil, "")); err == nil {
			t.Error("want error for unsupported type")
		}
	})

	t.Run("non-zip bytes are an error", func(t *testing.T) {
		if _, err := e.Extract(xlsx.ContentType, []byte("not a zip")); err == nil {
			t.Error("want error for non-zip content")
		}
	})
}
