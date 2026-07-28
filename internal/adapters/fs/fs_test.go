package fs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/fs"
	"github.com/jmurray2011/lore/internal/app"
)

func TestSourceWalk(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "# A")
	write("b.txt", "bee")
	write("note.bin", "binary-ish")
	write("doc.pdf", "%PDF-ish")
	write("report.docx", "zip-ish")
	write("sheet.xlsx", "zip-ish")
	write("rows.csv", "id,status")
	write(".secret.txt", "shh")
	write(".git/config", "[core]")
	write("sub/c.md", "cee")

	got := map[string]app.SourceItem{}
	err := fs.NewSource().Walk(context.Background(), root, func(it app.SourceItem) error {
		got[filepath.Base(strings.TrimPrefix(it.URI, "file://"))] = it
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if len(got) != 8 {
		t.Fatalf("want 8 items, got %d: %v", len(got), got)
	}
	if _, ok := got[".secret.txt"]; ok {
		t.Error("hidden file should be skipped")
	}
	if _, ok := got["config"]; ok {
		t.Error("contents of a hidden directory should be skipped")
	}
	if got["a.md"].ContentType != "text/markdown" {
		t.Errorf("a.md type = %q", got["a.md"].ContentType)
	}
	if content, err := got["a.md"].Open(); err != nil || string(content) != "# A" {
		t.Errorf("a.md Open() = %q, %v; want %q", content, err, "# A")
	}
	if got["a.md"].Fingerprint == "" {
		t.Error("a.md should carry a fingerprint")
	}
	if got["b.txt"].ContentType != "text/plain" {
		t.Errorf("b.txt type = %q", got["b.txt"].ContentType)
	}
	if got["c.md"].ContentType != "text/markdown" {
		t.Errorf("sub/c.md type = %q", got["c.md"].ContentType)
	}
	if got["note.bin"].ContentType != "application/octet-stream" {
		t.Errorf("note.bin type = %q (fs yields it; the extractor decides support)", got["note.bin"].ContentType)
	}
	if got["doc.pdf"].ContentType != "application/pdf" {
		t.Errorf("doc.pdf type = %q", got["doc.pdf"].ContentType)
	}
	if got["report.docx"].ContentType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("report.docx type = %q", got["report.docx"].ContentType)
	}
	if got["sheet.xlsx"].ContentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("sheet.xlsx type = %q", got["sheet.xlsx"].ContentType)
	}
	// Explicit, not mime.TypeByExtension: the OS mime table (the Windows registry,
	// /etc/mime.types) can map .csv to something else on some machines.
	if got["rows.csv"].ContentType != "text/csv" {
		t.Errorf("rows.csv type = %q", got["rows.csv"].ContentType)
	}
	if !strings.HasPrefix(got["a.md"].URI, "file://") {
		t.Errorf("URI = %q, want file:// scheme", got["a.md"].URI)
	}
}

func TestSourceWalkSingleFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "only.txt")
	if err := os.WriteFile(path, []byte("solo"), 0o600); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := fs.NewSource().Walk(context.Background(), path, func(app.SourceItem) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 item for single-file root, got %d", n)
	}
}

func TestSourceFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")

	fp := func(t *testing.T) string {
		t.Helper()
		var got string
		if err := fs.NewSource().Walk(context.Background(), path, func(it app.SourceItem) error {
			got = it.Fingerprint
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		return got
	}

	if err := os.WriteFile(path, []byte("original content"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := fp(t)
	if first != fp(t) {
		t.Error("fingerprint must be stable for unchanged content")
	}

	if err := os.WriteFile(path, []byte("different content!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := fp(t); changed == first {
		t.Error("fingerprint must change when content changes")
	}
}

func TestSourceWalkStopsOnFnError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("stop")
	if err := fs.NewSource().Walk(context.Background(), root, func(app.SourceItem) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("want fn error propagated, got %v", err)
	}
}
