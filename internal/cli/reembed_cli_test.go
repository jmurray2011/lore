package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportReEmbedCLI(t *testing.T) {
	src, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("alpha beta gamma delta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := exec(src, "init", "kb"); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	if _, code := exec(src, "add", "kb", p); code != 0 {
		t.Fatalf("add exit %d", code)
	}
	art := filepath.Join(dir, "kb.lore")
	if _, code := exec(src, "export", "kb", "-o", art); code != 0 {
		t.Fatalf("export exit %d", code)
	}

	// Import into a fresh store with --re-embed: vectors are rebuilt with the local
	// embedder, and the collection is immediately queryable.
	dst, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	if out, code := exec(dst, "import", art, "--re-embed"); code != 0 {
		t.Fatalf("import --re-embed exit %d: %s", code, out)
	}
	out, code := exec(dst, "query", "kb", "alpha", "--json")
	if code != 0 || !strings.Contains(out, "alpha beta gamma") {
		t.Fatalf("query after re-embed: exit %d, out %s", code, out)
	}
}
