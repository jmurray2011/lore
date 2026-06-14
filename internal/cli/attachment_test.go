package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/lore/internal/limitio"
)

// TestLoadAttachmentsRejectsOversizedFile bounds attachment reads: an oversized
// file must be refused before its bytes are base64-inflated into a provider
// request.
func TestLoadAttachmentsRejectsOversizedFile(t *testing.T) {
	orig := maxAttachmentBytes
	maxAttachmentBytes = 64
	defer func() { maxAttachmentBytes = orig }()

	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, bytes.Repeat([]byte("A"), 500), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadAttachments([]string{path}); !errors.Is(err, limitio.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge for oversized attachment, got %v", err)
	}
}
