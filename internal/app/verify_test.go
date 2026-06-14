package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestCheckerVerify(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	coll := mustCollection(t, "docs", space)

	doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	c0 := mustChunk(t, doc.ID, 0, "The sky is blue.")
	c1 := mustChunk(t, doc.ID, 1, "Grass is usually green.")
	docs := &fakeDocs{
		docs:   map[domain.DocumentID]domain.Document{doc.ID: *doc},
		chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0, c1.ID: c1},
	}
	catalog := app.NewCatalog(newFakeCollections(coll), docs, &fakeEmbedder{space: space}, domain.Registry{})

	// The unsupported map keys on the segmented claim text (markers stripped).
	verifier := &fakeVerifier{unsupported: map[string]bool{"Grass is purple": true}}
	checker := app.NewChecker(verifier, catalog)

	ans := app.Answer{
		Text: "The sky is blue [" + string(c0.ID) + "]. Grass is purple [" + string(c1.ID) + "]. Mars is red.",
		Citations: []domain.Citation{
			{ChunkID: c0.ID, Source: "file:///a.md", Seq: 0},
			{ChunkID: c1.ID, Source: "file:///a.md", Seq: 1},
		},
	}

	claims, err := checker.Verify(ctx, "docs", ans)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(claims) != 3 {
		t.Fatalf("want 3 claims, got %d: %+v", len(claims), claims)
	}
	if claims[0].Verdict != domain.VerdictSupported {
		t.Errorf("claim 0 should be supported, got %q", claims[0].Verdict)
	}
	if claims[1].Verdict != domain.VerdictUnsupported {
		t.Errorf("claim 1 should be unsupported, got %q", claims[1].Verdict)
	}
	if claims[2].Verdict != domain.VerdictUncited {
		t.Errorf("claim 2 (no citation) should be uncited, got %q", claims[2].Verdict)
	}
	// The evidence passed for claim 0 is its cited chunk's text.
	if verifier.gotEvidence["The sky is blue"] != "The sky is blue." {
		t.Errorf("claim 0 evidence = %q", verifier.gotEvidence["The sky is blue"])
	}
	// The uncited claim must not cost a model call: 2 cited claims → 2 calls.
	if verifier.calls != 2 {
		t.Errorf("want 2 verify calls (uncited skipped), got %d", verifier.calls)
	}
	if got := domain.SupportRate(claims); got != 1.0/3.0 {
		t.Errorf("support rate = %v, want 1/3", got)
	}
}
