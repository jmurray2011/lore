package domain_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestSegmentClaims(t *testing.T) {
	a := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///a.md"), 0)
	b := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///b.md"), 0)
	cites := []domain.Citation{{ChunkID: a}, {ChunkID: b}}

	t.Run("splits sentences and maps cited chunks, stripping markers", func(t *testing.T) {
		text := "The sky is blue [" + string(a) + "]. Grass is green [" + string(b) + "]. A claim with no citation."
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 3 {
			t.Fatalf("want 3 claims, got %d: %+v", len(claims), claims)
		}
		if claims[0].Text != "The sky is blue" || len(claims[0].CitedChunks) != 1 || claims[0].CitedChunks[0] != a {
			t.Errorf("claim 0 = %+v", claims[0])
		}
		if claims[1].CitedChunks[0] != b {
			t.Errorf("claim 1 cites = %v", claims[1].CitedChunks)
		}
		// The marker-less sentence shares the block, so it inherits its citations
		// and goes to the Verifier rather than being flagged uncited.
		if claims[2].Verdict != "" || len(claims[2].CitedChunks) != 2 {
			t.Errorf("claim 2 should inherit the block's citations unjudged, got %+v", claims[2])
		}
		// Cited claims start unjudged (the Verifier fills them in).
		if claims[0].Verdict != "" {
			t.Errorf("cited claim should start unjudged, got %q", claims[0].Verdict)
		}
	})

	t.Run("parses multiple citations in one marker", func(t *testing.T) {
		text := "Both sources agree [" + string(a) + ", " + string(b) + "]."
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 1 || len(claims[0].CitedChunks) != 2 {
			t.Fatalf("want 1 claim with 2 cites, got %+v", claims)
		}
	})

	t.Run("drops citations not in the grounding set", func(t *testing.T) {
		ghost := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///ghost.md"), 0)
		text := "Hallucinated source [" + string(ghost) + "]."
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 1 || len(claims[0].CitedChunks) != 0 || claims[0].Verdict != domain.VerdictUncited {
			t.Errorf("an unknown citation must be dropped, leaving the claim uncited: %+v", claims)
		}
	})

	t.Run("empty answer yields no claims", func(t *testing.T) {
		if claims := domain.SegmentClaims("   ", cites); len(claims) != 0 {
			t.Errorf("want no claims, got %+v", claims)
		}
	})

	t.Run("does not over-split on abbreviations, initials, or decimals", func(t *testing.T) {
		// One real sentence carrying "U.S.", "e.g.", and a decimal version. A bare
		// [.!?] split shatters this into fragments and orphans the citation, which
		// deflates the support rate on ordinary prose.
		text := "The service runs in the U.S. and is used by e.g. agencies on gpt-5.4 builds [" + string(a) + "]."
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 1 {
			t.Fatalf("want 1 claim (abbreviations/initials/decimals are not sentence ends), got %d: %+v", len(claims), claims)
		}
		if len(claims[0].CitedChunks) != 1 || claims[0].CitedChunks[0] != a {
			t.Errorf("the citation must stay attached to its claim, got %+v", claims[0])
		}
	})

	t.Run("still splits genuine sentence boundaries", func(t *testing.T) {
		text := "Keys rotate yearly [" + string(a) + "]. Backups run nightly [" + string(b) + "]."
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 2 {
			t.Fatalf("want 2 claims, got %d: %+v", len(claims), claims)
		}
		if claims[0].CitedChunks[0] != a || claims[1].CitedChunks[0] != b {
			t.Errorf("claims = %+v", claims)
		}
	})

	t.Run("drops bare list enumerators", func(t *testing.T) {
		// "1. The sky..." splits as "1" | "The sky..." because a digit before ". "
		// is a sentence end. The enumerator is list structure, not a claim; keeping
		// it (always unverifiable) deflates the support rate of every numbered-list
		// answer.
		text := "1. The sky is blue [" + string(a) + "].\n2. Grass is green [" + string(b) + "].\n"
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 2 {
			t.Fatalf("want 2 claims (enumerators are not claims), got %d: %+v", len(claims), claims)
		}
		if claims[0].Text != "The sky is blue" || claims[1].Text != "Grass is green" {
			t.Errorf("claim texts = %q, %q", claims[0].Text, claims[1].Text)
		}
		if len(claims[0].CitedChunks) != 1 || claims[0].CitedChunks[0] != a {
			t.Errorf("claim 0 cites = %v, want [%s]", claims[0].CitedChunks, a)
		}
		if len(claims[1].CitedChunks) != 1 || claims[1].CitedChunks[0] != b {
			t.Errorf("claim 1 cites = %v, want [%s]", claims[1].CitedChunks, b)
		}
	})

	t.Run("a marker-less sentence inherits its paragraph's citations", func(t *testing.T) {
		// Generators cite once at the end of a multi-sentence point; the earlier
		// sentences of the point carry no inline marker. They are grounded in the
		// same chunks, so they inherit the paragraph's citations and get verified
		// instead of being flagged uncited without a model call.
		text := "Writing with the model reduced diversity. The authors credit the model [" + string(a) + "].\n"
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 2 {
			t.Fatalf("want 2 claims, got %d: %+v", len(claims), claims)
		}
		if len(claims[0].CitedChunks) != 1 || claims[0].CitedChunks[0] != a {
			t.Errorf("claim 0 should inherit the paragraph citation, got %+v", claims[0])
		}
		if claims[0].Verdict != "" {
			t.Errorf("an inheriting claim goes to the Verifier unjudged, got %q", claims[0].Verdict)
		}
	})

	t.Run("inheritance does not cross a paragraph break", func(t *testing.T) {
		// A paragraph with no citations anywhere stays uncited: inheriting from a
		// neighboring paragraph would attribute evidence the sentence never drew on.
		text := "An overview sentence with no marker.\n\nKeys rotate yearly [" + string(a) + "].\n"
		claims := domain.SegmentClaims(text, cites)
		if len(claims) != 2 {
			t.Fatalf("want 2 claims, got %d: %+v", len(claims), claims)
		}
		if claims[0].Verdict != domain.VerdictUncited || len(claims[0].CitedChunks) != 0 {
			t.Errorf("claim 0 must stay uncited, got %+v", claims[0])
		}
		if len(claims[1].CitedChunks) != 1 || claims[1].CitedChunks[0] != a {
			t.Errorf("claim 1 cites = %v", claims[1].CitedChunks)
		}
	})
}

func TestSupportRate(t *testing.T) {
	claims := []domain.Claim{
		{Verdict: domain.VerdictSupported},
		{Verdict: domain.VerdictSupported},
		{Verdict: domain.VerdictUnsupported},
		{Verdict: domain.VerdictUncited},
	}
	if got := domain.SupportRate(claims); got != 0.5 {
		t.Errorf("support rate = %v, want 0.5 (2 of 4 supported)", got)
	}
	if got := domain.SupportRate(nil); got != 1 {
		t.Errorf("support rate of no claims should be 1 (vacuously faithful), got %v", got)
	}
	if !domain.AllSupported([]domain.Claim{{Verdict: domain.VerdictSupported}}) {
		t.Error("a single supported claim should be all-supported")
	}
	if domain.AllSupported(claims) {
		t.Error("a set with an unsupported claim is not all-supported")
	}
}
