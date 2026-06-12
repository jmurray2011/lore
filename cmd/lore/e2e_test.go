package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestEndToEnd builds the real binary and drives init→add→ls→status→query→ask→rm
// across separate process invocations sharing one SQLite file. It exercises the
// composition root, the openai adapter's real HTTP path (against a stub
// provider), persistence across processes (which only holds with sqlite wired
// in — memstore is per-process), and ingestion of all wired document formats
// (text, docx, pdf) through the extractor router.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds the binary and runs it as subprocesses")
	}

	const dims = 8
	provider, chat := stubProvider(t, dims)
	defer provider.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "lore")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build binary: %v", err)
	}

	dbPath := filepath.Join(dir, "lore.db")
	env := append(os.Environ(),
		"LORE_BASE_URL="+provider.URL+"/v1",
		"LORE_API_KEY=test",
		"LORE_EMBED_MODEL=stub-embed",
		"LORE_CHAT_MODEL=stub-chat",
		"LORE_DIMENSIONS="+strconv.Itoa(dims),
		"LORE_STORAGE_BACKEND=sqlite",
		"LORE_DB_PATH="+dbPath,
		"LORE_IMAGE_INPUT=true",
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"), // isolate from any real config.toml
	)

	run := func(t *testing.T, args ...string) (stdout, stderr string, code int) {
		t.Helper()
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			code = 0
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			t.Fatalf("exec %v: %v", args, err)
		}
		return out.String(), errb.String(), code
	}

	mustSucceed := func(t *testing.T, args ...string) string {
		t.Helper()
		out, errb, code := run(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d, stderr=%s", args, code, errb)
		}
		return out
	}

	txtPath := filepath.Join(dir, "alpha.txt")
	if err := os.WriteFile(txtPath, []byte("alpha beta gamma delta epsilon"), 0o600); err != nil {
		t.Fatal(err)
	}
	docxPath := filepath.Join(dir, "report.docx")
	if err := os.WriteFile(docxPath, docxBytes(t, "docxsentinel alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(dir, "paper.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes(t, "pdfsentinel alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	xlsxPath := filepath.Join(dir, "sheet.xlsx")
	if err := os.WriteFile(xlsxPath, xlsxBytes(t, "xlsxsentinel alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	mustSucceed(t, "init", "docs")
	mustSucceed(t, "add", "docs", txtPath, docxPath, pdfPath, xlsxPath)

	// Fresh process must see the collection written by a prior process.
	if out := mustSucceed(t, "--json", "ls"); !strings.Contains(out, `"name": "docs"`) {
		t.Fatalf("ls did not persist collection across processes: %s", out)
	}
	if out := mustSucceed(t, "--json", "status", "docs"); !strings.Contains(out, "stub-embed") {
		t.Fatalf("status missing pinned model: %s", out)
	}
	// docs lists every ingested source (persisted via sqlite ListDocuments).
	if out := mustSucceed(t, "--json", "docs", "docs"); !strings.Contains(out, "alpha.txt") ||
		!strings.Contains(out, "report.docx") || !strings.Contains(out, "paper.pdf") || !strings.Contains(out, "sheet.xlsx") {
		t.Fatalf("docs did not list all ingested sources: %s", out)
	}
	// sync with no path replays the source roots remembered (in sqlite) by add,
	// across processes; everything is unchanged so nothing is re-added.
	if out := mustSucceed(t, "--json", "sync", "docs"); !strings.Contains(out, `"added": 0`) {
		t.Fatalf("sync should replay remembered sources and re-add nothing: %s", out)
	}
	// All three formats flowed through the router into the store.
	out := mustSucceed(t, "--json", "query", "docs", "alpha")
	for _, want := range []string{"alpha beta gamma", "docxsentinel", "pdfsentinel", "xlsxsentinel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("query missing %q (format not ingested?): %s", want, out)
		}
	}
	if out := mustSucceed(t, "--json", "ask", "docs", "what is alpha?"); !strings.Contains(out, "stub answer") {
		t.Fatalf("ask did not return the synthesized answer: %s", out)
	}

	// Attachment path: with image_input enabled, --attach reaches the provider
	// as a multimodal image_url part (config flag → caps → generator → HTTP).
	imgPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}, 0o600); err != nil {
		t.Fatal(err)
	}
	mustSucceed(t, "ask", "docs", "describe the image", "--attach", imgPath)
	if body := chat.last(); !strings.Contains(body, `"type":"image_url"`) || !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("attachment did not reach the provider as image_url: %s", body)
	}

	mustSucceed(t, "rm", "docs")
	if out := mustSucceed(t, "--json", "ls"); strings.Contains(out, `"name": "docs"`) {
		t.Fatalf("collection survived rm: %s", out)
	}
}

// chatRecorder captures the most recent /v1/chat/completions request body so a
// test can assert what the binary actually sent.
type chatRecorder struct {
	mu   sync.Mutex
	body string
}

func (c *chatRecorder) record(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = string(b)
}

func (c *chatRecorder) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

// stubProvider serves an OpenAI-compatible /v1/embeddings and
// /v1/chat/completions. Every embedding is the same unit vector, which is all
// retrieval needs to surface the ingested chunk. It returns a recorder for the
// last chat request body.
func stubProvider(t *testing.T, dims int) (*httptest.Server, *chatRecorder) {
	t.Helper()
	mux := http.NewServeMux()
	chat := &chatRecorder{}

	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vec := make([]float32, dims)
		if dims > 0 {
			vec[0] = 1
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var resp struct {
			Data []datum `json:"data"`
		}
		for i := range req.Input {
			resp.Data = append(resp.Data, datum{Index: i, Embedding: vec})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		chat.record(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"stub answer grounded in context"}}]}`)
	})

	return httptest.NewServer(mux), chat
}

// docxBytes builds a minimal .docx (zip with word/document.xml) carrying text.
func docxBytes(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	doc := `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`
	if _, err := io.WriteString(w, doc); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// xlsxBytes builds a minimal .xlsx (shared-string table + one worksheet cell).
func xlsxBytes(t *testing.T, text string) []byte {
	t.Helper()
	const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
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
	write("xl/sharedStrings.xml", `<?xml version="1.0"?><sst `+ns+`><si><t>`+text+`</t></si></sst>`)
	write("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet `+ns+`><sheetData><row><c t="s"><v>0</v></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pdfBytes builds a minimal single-page PDF showing text, with correct xref
// offsets so the fixture is valid.
func pdfBytes(t *testing.T, text string) []byte {
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
