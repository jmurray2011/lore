package fs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/limitio"
)

// TestWalkOpenRejectsOversizedFile bounds whole-file ingest reads: opening a
// file larger than the cap must fail rather than load it all into memory.
func TestWalkOpenRejectsOversizedFile(t *testing.T) {
	orig := maxFileBytes
	maxFileBytes = 64
	defer func() { maxFileBytes = orig }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), bytes.Repeat([]byte("A"), 500), 0o600); err != nil {
		t.Fatal(err)
	}

	var item app.SourceItem
	if err := (Source{}).Walk(context.Background(), dir, func(it app.SourceItem) error {
		item = it
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if _, err := item.Open(); !errors.Is(err, limitio.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge opening oversized file, got %v", err)
	}
}
