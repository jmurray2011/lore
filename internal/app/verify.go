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
	claims := domain.SegmentClaims(ans.Text, ans.Citations)
	textByID, err := c.chunkText(ctx, collection, ans.Citations)
	if err != nil {
		return nil, err
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
