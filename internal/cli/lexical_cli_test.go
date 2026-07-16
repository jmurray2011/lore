package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryAndAskLexicalCLI(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})

	// Seed through the normal ingest path, which populates the lexical index too.
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("alpha beta gamma delta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := exec(deps, "init", "kb"); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	if _, code := exec(deps, "add", "kb", p); code != 0 {
		t.Fatalf("add exit %d", code)
	}

	out, code := exec(deps, "query", "kb", "alpha", "--lexical", "--json")
	if code != 0 {
		t.Fatalf("query --lexical exit %d: %s", code, out)
	}
	if !strings.Contains(out, "alpha beta gamma") {
		t.Errorf("want the lexical hit in output, got: %s", out)
	}

	// ask --lexical grounds on BM25 hits (no embedding) and still synthesizes.
	if out, code := exec(deps, "ask", "kb", "alpha", "--lexical", "--json"); code != 0 {
		t.Fatalf("ask --lexical exit %d: %s", code, out)
	}

	// --lexical is mutually exclusive with the vector-based modes.
	if _, code := exec(deps, "query", "kb", "alpha", "--lexical", "--hybrid"); code != 2 {
		t.Errorf("want exit 2 for --lexical --hybrid, got %d", code)
	}
}
