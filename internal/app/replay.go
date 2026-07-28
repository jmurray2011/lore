package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

// Replayer re-runs an ask Manifest to prove its answer reproduces: it checks the
// corpus has not drifted (each collection's CorpusSnapshot digest still matches),
// re-runs the recorded retrieval to confirm the cited evidence still surfaces,
// and re-synthesizes from the pinned cited chunks to confirm the answer text
// reproduces. It is the verification half of the provenance exhibit.
type Replayer struct {
	catalog   *Catalog
	retriever *Retriever
	asker     *Asker
	docs      DocumentRepository
}

// NewReplayer wires a Replayer over the catalog (corpus digests), the retriever
// (re-retrieval), the asker (re-synthesis), and the document repository (to
// rehydrate the pinned cited chunks into grounding hits).
func NewReplayer(catalog *Catalog, retriever *Retriever, asker *Asker, docs DocumentRepository) *Replayer {
	return &Replayer{catalog: catalog, retriever: retriever, asker: asker, docs: docs}
}

// CorpusDrift names a collection whose content digest changed since the manifest
// was recorded — the corpus the exhibit was grounded in no longer exists.
type CorpusDrift struct {
	Collection string
	Was        domain.ContentHash
	Now        domain.ContentHash
}

// ReplayReport is the outcome of re-running a manifest. Attempted is false when
// corpus drift aborted reproduction (the fail-closed default); RetrievalMatch is
// true when every cited chunk still surfaced in re-retrieval; AnswerMatch is true
// when the re-synthesized answer's digest equals the manifest's.
type ReplayReport struct {
	Drift          []CorpusDrift
	Attempted      bool
	RetrievalMatch bool
	MissingCited   []string
	AnswerMatch    bool
	AnswerDigest   domain.ContentHash
	ExpectedAnswer domain.ContentHash
}

// Reproduced reports whether the exhibit faithfully reproduced. Drift fails it
// unless allowDrift was set (the caller explicitly accepted a changed corpus);
// otherwise it requires that reproduction was attempted and both the retrieval
// and the answer matched.
func (r ReplayReport) Reproduced(allowDrift bool) bool {
	if len(r.Drift) > 0 && !allowDrift {
		return false
	}
	return r.Attempted && r.RetrievalMatch && r.AnswerMatch
}

// Replay re-runs the manifest. With drift present and allowDrift false it stops
// after the digest check (fail closed: an exhibit cannot be faithfully
// reproduced against a changed corpus); otherwise it re-retrieves and
// re-synthesizes and reports both fidelities. A missing collection or repository
// error is returned as-is.
func (r *Replayer) Replay(ctx context.Context, m Manifest, allowDrift bool) (ReplayReport, error) {
	report := ReplayReport{ExpectedAnswer: m.AnswerDigest}

	current, err := CorpusRefsOf(ctx, r.catalog, collectionsOf(m))
	if err != nil {
		return ReplayReport{}, err
	}
	was := make(map[string]domain.ContentHash, len(m.Corpus))
	for _, c := range m.Corpus {
		was[c.Collection] = c.Digest
	}
	for _, c := range current {
		if prior := was[c.Collection]; prior != c.Digest {
			report.Drift = append(report.Drift, CorpusDrift{Collection: c.Collection, Was: prior, Now: c.Digest})
		}
	}
	if len(report.Drift) > 0 && !allowDrift {
		return report, nil // fail closed: do not attempt reproduction against a changed corpus
	}
	report.Attempted = true

	// Retrieval fidelity: does the same question still surface the cited evidence?
	opts, err := retrieveOptionsFromManifest(m)
	if err != nil {
		return report, err
	}
	hits, _, err := r.retriever.Resolve(ctx, opts)
	if err != nil {
		return report, fmt.Errorf("replay re-retrieve: %w", err)
	}
	retrieved := make(map[string]bool, len(hits))
	for _, h := range hits {
		retrieved[string(h.Chunk.ID)] = true
	}
	for _, id := range m.CitedChunks {
		if !retrieved[id] {
			report.MissingCited = append(report.MissingCited, id)
		}
	}
	report.RetrievalMatch = len(report.MissingCited) == 0

	// Answer fidelity: does the same evidence still yield the same answer?
	pinned, err := r.hitsFromChunkIDs(ctx, m.CitedChunks)
	if err != nil {
		return report, fmt.Errorf("replay rehydrate cited chunks: %w", err)
	}
	var ans Answer
	if m.Generation.Deterministic {
		ans, err = r.asker.SynthesizeReproducible(ctx, m.Question, pinned, nil)
	} else {
		ans, err = r.asker.Synthesize(ctx, m.Question, pinned, nil)
	}
	if err != nil {
		return report, fmt.Errorf("replay re-synthesize: %w", err)
	}
	report.AnswerDigest = domain.HashContent([]byte(ans.Text))
	report.AnswerMatch = report.AnswerDigest == m.AnswerDigest
	return report, nil
}

// hitsFromChunkIDs rehydrates pinned chunk IDs into grounding hits, attaching
// each chunk's document source URI so synthesis can cite it — mirroring the
// Querier's hydration of retrieval matches.
func (r *Replayer) hitsFromChunkIDs(ctx context.Context, ids []string) ([]domain.ChunkHit, error) {
	cids := make([]domain.ChunkID, len(ids))
	for i, id := range ids {
		cids[i] = domain.ChunkID(id)
	}
	chunks, err := r.docs.GetChunks(ctx, cids)
	if err != nil {
		return nil, err
	}
	docIDs := make([]domain.DocumentID, 0, len(chunks))
	seen := make(map[domain.DocumentID]bool, len(chunks))
	for _, c := range chunks {
		if !seen[c.DocumentID] {
			seen[c.DocumentID] = true
			docIDs = append(docIDs, c.DocumentID)
		}
	}
	documents, err := r.docs.GetDocuments(ctx, docIDs)
	if err != nil {
		return nil, err
	}
	source := make(map[domain.DocumentID]string, len(documents))
	for _, d := range documents {
		source[d.ID] = d.SourceURI
	}
	hits := make([]domain.ChunkHit, 0, len(chunks))
	for _, c := range chunks {
		hits = append(hits, domain.ChunkHit{Chunk: c, Source: source[c.DocumentID]})
	}
	return hits, nil
}

// collectionsOf returns the manifest's collections in recorded (query) order.
func collectionsOf(m Manifest) []string {
	cols := make([]string, len(m.Corpus))
	for i, c := range m.Corpus {
		cols[i] = c.Collection
	}
	return cols
}

// retrieveOptionsFromManifest reconstructs the RetrieveOptions that produced the
// manifest, so replay re-runs an identical retrieval pipeline.
func retrieveOptionsFromManifest(m Manifest) (RetrieveOptions, error) {
	filter, err := domain.ParseWhere(m.Retrieval.Where)
	if err != nil {
		return RetrieveOptions{}, err
	}
	return RetrieveOptions{
		Collections:  collectionsOf(m),
		Query:        m.Question,
		K:            m.Retrieval.K,
		Candidates:   m.Retrieval.Candidates,
		Source:       m.Retrieval.Source,
		Filter:       filter,
		Rerank:       m.Retrieval.Rerank,
		Hybrid:       m.Retrieval.Hybrid,
		MMR:          m.Retrieval.MMR,
		MMRLambda:    m.Retrieval.MMRLambda,
		Recency:      m.Retrieval.Recency,
		HalfLife:     time.Duration(m.Retrieval.HalfLifeDays * float64(24*time.Hour)),
		MaxPerSource: m.Retrieval.MaxPerSource,
	}, nil
}
