package cli

import (
	"context"
	"sort"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// buildAskManifest assembles the reproducible provenance manifest for an ask:
// the per-collection corpus snapshot digests, the resolved retrieval config, the
// generation identity, the chunks the answer cited, and a digest of the answer
// text. It is built only under --json (see the ask RunE); `lore replay` consumes
// it to re-run the exhibit. The generation identity beyond the configured model
// name (resolved model, fingerprint, determinism) rides on ans.Provenance when
// the adapter captured it.
func buildAskManifest(ctx context.Context, deps *Deps, collections []string, question string, rm app.RetrievalManifest, ans app.Answer) (*app.Manifest, error) {
	refs, err := app.CorpusRefsOf(ctx, deps.Catalog, collections)
	if err != nil {
		return nil, err
	}
	gen := app.GenerationManifest{ChatModel: deps.ChatModel}
	if p := ans.Provenance; p != nil {
		gen.ResolvedModel = p.ResolvedModel
		gen.SystemFingerprint = p.SystemFingerprint
		gen.Deterministic = p.Deterministic
		if p.Deterministic {
			temp, seed := p.Temperature, p.Seed
			gen.Temperature, gen.Seed = &temp, &seed
		}
	}
	return &app.Manifest{
		Question:     question,
		AskedAt:      time.Now().UTC(),
		Corpus:       refs,
		Retrieval:    rm,
		Generation:   gen,
		CitedChunks:  manifestCitedChunks(ans),
		AnswerDigest: domain.HashContent([]byte(ans.Text)),
	}, nil
}

// manifestCitedChunks returns the answer's cited chunk IDs, deduplicated and
// sorted, so the manifest is stable regardless of citation order.
func manifestCitedChunks(ans app.Answer) []string {
	seen := make(map[string]struct{}, len(ans.Citations))
	ids := make([]string, 0, len(ans.Citations))
	for _, c := range ans.Citations {
		id := string(c.ChunkID)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
