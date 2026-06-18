package app

import (
	"context"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

// Manifest is the reproducible provenance record of one `ask`: the corpus state,
// retrieval configuration, and generation identity that produced an answer, the
// chunks it actually cited, and a digest of the answer text. It turns "trust me,
// it was grounded" into an auditable, re-runnable exhibit: `lore replay` checks
// the corpus has not drifted, re-runs the recorded retrieval, and re-synthesizes
// from the cited chunks to prove the answer reproduces.
type Manifest struct {
	Question     string             `json:"question"`
	AskedAt      time.Time          `json:"asked_at"`
	Corpus       []CorpusRef        `json:"corpus"`
	Retrieval    RetrievalManifest  `json:"retrieval"`
	Generation   GenerationManifest `json:"generation"`
	CitedChunks  []string           `json:"cited_chunks"`
	AnswerDigest domain.ContentHash `json:"answer_digest"`
}

// CorpusRef pins one collection's content identity (CorpusSnapshot digest) and
// embedding space at ask time. Replay compares each against the live collection;
// a changed digest means the corpus drifted and the exhibit cannot be reproduced.
type CorpusRef struct {
	Collection string             `json:"collection"`
	Digest     domain.ContentHash `json:"digest"`
	Model      string             `json:"embedding_model"`
	Dimensions int                `json:"dimensions"`
}

// RetrievalManifest records the retrieval levers exactly as resolved, so replay
// reconstructs the same RetrieveOptions and re-runs an identical pipeline.
type RetrievalManifest struct {
	K            int      `json:"k"`
	Candidates   int      `json:"candidates,omitempty"`
	Source       string   `json:"source,omitempty"`
	Where        []string `json:"where,omitempty"`
	Rerank       bool     `json:"rerank,omitempty"`
	Hybrid       bool     `json:"hybrid,omitempty"`
	MMR          bool     `json:"mmr,omitempty"`
	MMRLambda    float64  `json:"mmr_lambda,omitempty"`
	Recency      bool     `json:"recency,omitempty"`
	HalfLifeDays float64  `json:"half_life_days,omitempty"`
	MaxPerSource int      `json:"max_per_source,omitempty"`
	Budget       int      `json:"budget,omitempty"`
}

// GenerationManifest records the generation identity. ChatModel is the
// configured model name (always present). ResolvedModel and SystemFingerprint
// are the provider's response-reported identity, present when the adapter
// captured them. Deterministic, Temperature, and Seed are set only for a
// --reproducible run that pinned them; nil Temperature/Seed means the run used
// the provider default (non-deterministic).
type GenerationManifest struct {
	ChatModel         string   `json:"chat_model"`
	ResolvedModel     string   `json:"resolved_model,omitempty"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
	Deterministic     bool     `json:"deterministic"`
	Temperature       *float64 `json:"temperature,omitempty"`
	Seed              *int     `json:"seed,omitempty"`
}

// CorpusRefsOf builds a CorpusRef for each named collection, in the given order
// (the query order — primary collection first — not sorted), so a manifest
// records what was asked. Each ref's digest is order-independent within the
// collection (CorpusSnapshot sorts its documents). An unknown collection returns
// the catalog's error (ErrNotFound), naming nothing the caller has not asked for.
func CorpusRefsOf(ctx context.Context, cat *Catalog, collections []string) ([]CorpusRef, error) {
	refs := make([]CorpusRef, 0, len(collections))
	for _, name := range collections {
		coll, err := cat.Get(ctx, name)
		if err != nil {
			return nil, err
		}
		docs, err := cat.ListDocuments(ctx, name)
		if err != nil {
			return nil, err
		}
		refs = append(refs, CorpusRef{
			Collection: name,
			Digest:     domain.SnapshotOf(docs).Digest,
			Model:      coll.Space.Model,
			Dimensions: coll.Space.Dimensions,
		})
	}
	return refs, nil
}
