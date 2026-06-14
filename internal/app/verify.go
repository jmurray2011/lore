package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmurray2011/lore/internal/domain"
)

// Checker verifies an answer's faithfulness: it segments the answer into claims
// and judges whether each claim is entailed by the chunks it cites, via the
// Verifier port. It is the use case behind `ask --verify`.
type Checker struct {
	verifier Verifier
	catalog  *Catalog
}

// NewChecker wires a Checker over a Verifier and the Catalog (used to fetch the
// cited chunks' text as evidence).
func NewChecker(verifier Verifier, catalog *Catalog) *Checker {
	return &Checker{verifier: verifier, catalog: catalog}
}

// Verify returns the answer's claims with their verdicts filled in. collection is
// the default collection for citations that carry no origin (single-collection
// answers); a citation with its own Collection (cross-collection answers) is
// resolved against that. An uncited claim is left flagged (VerdictUncited) and
// costs no model call; each cited claim is judged against the concatenated text of
// the chunks it cites. The aggregate support rate is domain.SupportRate(claims).
func (c *Checker) Verify(ctx context.Context, collection string, ans Answer) ([]domain.Claim, error) {
	textByID, err := c.chunkText(ctx, collection, ans.Citations)
	if err != nil {
		return nil, err
	}
	return c.verifyClaims(ctx, ans, textByID)
}

// VerifyWithEvidence verifies ans using caller-supplied chunk text as the
// evidence (keyed by chunk ID) instead of fetching it from a collection. It backs
// `synthesize --verify`, where the grounding chunks are piped in and may not live
// in any collection (an external retriever, hand-assembled context). Semantics
// otherwise match Verify, including the grounding-set fallback for markerless
// answers.
func (c *Checker) VerifyWithEvidence(ctx context.Context, ans Answer, evidenceByID map[domain.ChunkID]string) ([]domain.Claim, error) {
	return c.verifyClaims(ctx, ans, evidenceByID)
}

// verifyClaims segments ans into claims and judges each cited claim against the
// supplied evidence (chunk ID → text). When the answer carries citations but used
// no inline [id] markers (every claim came back uncited), it mirrors the
// generator's grounding-set fallback — verifying each claim against the whole
// cited set rather than reporting a spurious 0% support rate. Uncited claims cost
// no model call.
func (c *Checker) verifyClaims(ctx context.Context, ans Answer, textByID map[domain.ChunkID]string) ([]domain.Claim, error) {
	claims := domain.SegmentClaims(ans.Text, ans.Citations)
	if len(claims) > 0 && len(ans.Citations) > 0 && !anyCited(claims) {
		ground := citationIDs(ans.Citations)
		for i := range claims {
			claims[i].CitedChunks = ground
			claims[i].Verdict = ""
		}
	}
	for i := range claims {
		if claims[i].Verdict != "" {
			continue // uncited: nothing to verify
		}
		evidence := evidenceText(claims[i].CitedChunks, textByID)
		v, err := c.verifier.Verify(ctx, claims[i].Text, evidence)
		if err != nil {
			return nil, fmt.Errorf("verify claim: %w", err)
		}
		if v.Supported {
			claims[i].Verdict = domain.VerdictSupported
		} else {
			claims[i].Verdict = domain.VerdictUnsupported
		}
		claims[i].Rationale = v.Rationale
	}
	return claims, nil
}

// anyCited reports whether any claim was anchored to an inline citation.
func anyCited(claims []domain.Claim) bool {
	for _, cl := range claims {
		if len(cl.CitedChunks) > 0 {
			return true
		}
	}
	return false
}

// citationIDs returns the distinct chunk IDs of the citations in first-appearance
// order — the grounding set a fallback verifies every claim against.
func citationIDs(citations []domain.Citation) []domain.ChunkID {
	ids := make([]domain.ChunkID, 0, len(citations))
	seen := make(map[domain.ChunkID]bool, len(citations))
	for _, cit := range citations {
		if !seen[cit.ChunkID] {
			seen[cit.ChunkID] = true
			ids = append(ids, cit.ChunkID)
		}
	}
	return ids
}

// chunkText fetches the text of every cited chunk, grouping citation IDs by their
// origin collection (defaulting to collection when a citation carries none), and
// returns a map from chunk ID to its text.
func (c *Checker) chunkText(ctx context.Context, collection string, citations []domain.Citation) (map[domain.ChunkID]string, error) {
	byColl := make(map[string][]string)
	for _, cit := range citations {
		coll := cit.Collection
		if coll == "" {
			coll = collection
		}
		byColl[coll] = append(byColl[coll], string(cit.ChunkID))
	}
	out := make(map[domain.ChunkID]string)
	for coll, ids := range byColl {
		chunks, err := c.catalog.ChunksByIDs(ctx, coll, ids)
		if err != nil {
			return nil, fmt.Errorf("fetch evidence chunks: %w", err)
		}
		for _, ch := range chunks {
			out[ch.ID] = ch.Text
		}
	}
	return out, nil
}

// evidenceText concatenates the text of the cited chunks into one evidence block.
func evidenceText(ids []domain.ChunkID, textByID map[domain.ChunkID]string) string {
	var parts []string
	for _, id := range ids {
		if t := textByID[id]; t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}
