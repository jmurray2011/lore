package xlsx_test

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/xlsx"
)

const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`

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
