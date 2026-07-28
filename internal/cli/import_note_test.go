package cli_test

import (
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/cli"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestImportQueryabilityNote(t *testing.T) {
	local := domain.EmbeddingSpace{Model: "local-embed", Dimensions: 768}

	// A mismatch tells the recipient exactly what an embedder must serve to query.
	msg, ok := cli.ImportQueryabilityNote(local, "text-embedding-3-small", 1536)
	if !ok {
		t.Fatal("want a note when the local embedder cannot query the imported space")
	}
	for _, want := range []string{"text-embedding-3-small", "1536", "embedder"} {
		if !strings.Contains(msg, want) {
			t.Errorf("note missing %q: %s", want, msg)
		}
	}

	// No note when the local embedder already serves the collection's space.
	if _, ok := cli.ImportQueryabilityNote(local, "local-embed", 768); ok {
		t.Error("no note expected when the local embedder already serves the space")
	}

	// No note when the local space is unknown (don't nag on missing info).
	if _, ok := cli.ImportQueryabilityNote(domain.EmbeddingSpace{}, "m", 1); ok {
		t.Error("no note expected when the local space is unset")
	}
}
