package artifact

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/limitio"
)

// TestReadBoundsDecode guards against an oversized (or maliciously framed)
// artifact: encoding/gob is not hardened against adversarial input, so Read
// caps the bytes it will pull from an untrusted stream.
func TestReadBoundsDecode(t *testing.T) {
	orig := maxArtifactBytes
	maxArtifactBytes = 64
	defer func() { maxArtifactBytes = orig }()

	// A well-framed artifact whose gob body exceeds the cap must be refused.
	b := Bundle{Collection: Collection{Name: "kb", Sources: []string{strings.Repeat("x", 500)}}}
	var buf bytes.Buffer
	if err := Write(&buf, b); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := Read(&buf); !errors.Is(err, limitio.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge for oversized artifact, got %v", err)
	}
}
