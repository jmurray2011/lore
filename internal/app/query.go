package app

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jmurray2011/lore/internal/domain"
)

// Querier retrieves the chunks most similar to a query within a Collection.
type Querier struct {
	collections CollectionRepository
	index       VectorIndex
	docs        DocumentRepository
	embedder    Embedder
}

// NewQuerier wires a Querier from the ports it needs.
func NewQuerier(collections CollectionRepository, index VectorIndex, docs DocumentRepository, embedder Embedder) *Querier {
	return &Querier{collections: collections, index: index, docs: docs, embedder: embedder}
}

// sourceOverfetch multiplies k when a source filter is set, so enough candidates
// are retrieved for the filter to fill k. It is best-effort: if matching
// documents are buried below the over-fetch window (one document dominates the
// top results), some matches can still be missed.
const sourceOverfetch = 10

// Retrieval is what Explain returns: the top-k Hits plus the best candidate just
// outside them (the runner-up), for --explain diagnostics. HasNext is false when
// there is no further candidate (fewer than k+1 matched after filtering).
type Retrieval struct {
	Hits      []domain.ChunkHit
	NextScore float64
	HasNext   bool
}

// FromQuery is one source-collection chunk used directly as a query vector and
// the target-collection hits it retrieved. It backs `query --from-collection`,
// which feeds a collection's own stored vectors back as queries (never
// re-embedding) to find each chunk's nearest neighbors in another collection.
type FromQuery struct {
	From domain.ChunkHit   // the source chunk's identity + source URI (Score unused)
	Hits []domain.ChunkHit // its target hits, best first
}

// Query embeds the question and returns up to k ChunkHits from the collection,
// best match first. It enforces space coherence (invariant 1): the embedder
// must produce vectors in the collection's space, or it fails with
// ErrSpaceMismatch before touching the index.
//
// A non-empty source restricts hits to documents whose source URI matches that
// glob (matched against the basename, or the path when the pattern contains a
// "/"); Query over-fetches to fill k, approximately (see sourceOverfetch).
//
// An empty query is ErrInvalidArgument; an unknown collection is ErrNotFound.
// No matching chunks yields no hits and no error.
func (q *Querier) Query(ctx context.Context, collection, query string, k int, source string) ([]domain.ChunkHit, error) {
	r, err := q.retrieve(ctx, collection, query, k, source, false)
	return r.Hits, err
}

// Explain runs the same retrieval as Query but also reports the best candidate
// just outside the returned top-k (the runner-up) for --explain diagnostics. It
// fetches one extra candidate (k+1) to surface that runner-up — no extra model
// call beyond the single query embed. Same errors as Query.
func (q *Querier) Explain(ctx context.Context, collection, query string, k int, source string) (Retrieval, error) {
	return q.retrieve(ctx, collection, query, k, source, true)
}

// QueryAcross merges retrieval over several collections into one ranked top-k.
// All collections must share an embedding space (domain.SameSpace) — vectors are
// only comparable within a space — so the query is embedded once, each
// collection is searched, and the hits are merged by score. Each returned hit
// carries its origin Collection. Same per-collection source/over-fetch semantics
// as Query. Mismatched spaces fail with ErrSpaceMismatch before any search.
func (q *Querier) QueryAcross(ctx context.Context, collections []string, query string, k int, source string) ([]domain.ChunkHit, error) {
	r, err := q.retrieveAcross(ctx, collections, query, k, source, false)
	return r.Hits, err
}

// ExplainAcross is QueryAcross with the runner-up just outside the merged top-k,
// for --explain over a multi-collection query.
func (q *Querier) ExplainAcross(ctx context.Context, collections []string, query string, k int, source string) (Retrieval, error) {
	return q.retrieveAcross(ctx, collections, query, k, source, true)
}

// QueryFrom searches the target collection using the source collection's own
// stored chunk vectors as queries — one search per source chunk, grouped by
// source chunk. Nothing is re-embedded: source vectors are read straight from
// the index, so the comparison is sound only if source and target share an
// embedding space, which is enforced (domain.SameSpace → ErrSpaceMismatch)
// before any index access. A non-empty source glob restricts the *target* hits.
// An unknown source or target collection is ErrNotFound; an empty source
// collection yields no groups.
func (q *Querier) QueryFrom(ctx context.Context, target, sourceCollection string, k int, source string) ([]FromQuery, error) {
	// Resolve and space-check before touching the index: a mismatch must perform
	// neither the Entries read nor any Search.
	tgt, err := q.collections.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	src, err := q.collections.Get(ctx, sourceCollection)
	if err != nil {
		return nil, err
	}
	if err := domain.SameSpace([]*domain.Collection{tgt, src}); err != nil {
		return nil, err
	}

	entries, err := q.index.Entries(ctx, src.Name)
	if err != nil {
		return nil, fmt.Errorf("read %q vectors: %w", src.Name, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// Hydrate the source chunks for their provenance (seq + source URI), used to
	// label and order the groups; their vectors come straight from the index.
	vecByID := make(map[domain.ChunkID][]float32, len(entries))
	ids := make([]domain.ChunkID, len(entries))
	for i, e := range entries {
		vecByID[e.ChunkID] = e.Vector
		ids[i] = e.ChunkID
	}
	srcChunks, err := q.docs.GetChunks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate source chunks: %w", err)
	}
	srcURIs, err := q.sources(ctx, srcChunks)
	if err != nil {
		return nil, err
	}
	// Stable group order: by source URI, then by chunk seq within the document.
	sort.Slice(srcChunks, func(i, j int) bool {
		ui, uj := srcURIs[srcChunks[i].DocumentID], srcURIs[srcChunks[j].DocumentID]
		if ui != uj {
			return ui < uj
		}
		return srcChunks[i].Seq < srcChunks[j].Seq
	})

	searchK := searchBudget(k, source, false)
	groups := make([]FromQuery, 0, len(srcChunks))
	for _, sc := range srcChunks {
		matches, err := q.index.Search(ctx, tgt.Name, vecByID[sc.ID], searchK)
		if err != nil {
			return nil, fmt.Errorf("search %q: %w", tgt.Name, err)
		}
		hits, err := q.hydrate(ctx, matches, nil, source)
		if err != nil {
			return nil, err
		}
		if k > 0 && len(hits) > k {
			hits = hits[:k]
		}
		groups = append(groups, FromQuery{
			From: domain.ChunkHit{Chunk: sc, Source: srcURIs[sc.DocumentID]},
			Hits: hits,
		})
	}
	return groups, nil
}

// retrieve is the shared body of Query and Explain. withRunnerUp fetches one
// extra candidate (non-source path) and splits the ordered, filtered candidates
// into the returned top-k and the runner-up. Query passes false, so its search
// budget and result are byte-for-byte what they were before Explain existed.
func (q *Querier) retrieve(ctx context.Context, collection, query string, k int, source string, withRunnerUp bool) (Retrieval, error) {
	if strings.TrimSpace(query) == "" {
		return Retrieval{}, fmt.Errorf("query: %w: text must not be empty", domain.ErrInvalidArgument)
	}

	coll, err := q.collections.Get(ctx, collection)
	if err != nil {
		return Retrieval{}, err
	}

	vec, err := q.embedQuery(ctx, query, coll)
	if err != nil {
		return Retrieval{}, err
	}

	matches, err := q.index.Search(ctx, coll.Name, vec, searchBudget(k, source, withRunnerUp))
	if err != nil {
		return Retrieval{}, fmt.Errorf("search %q: %w", coll.Name, err)
	}
	if len(matches) == 0 {
		return Retrieval{}, nil
	}

	hits, err := q.hydrate(ctx, matches, nil, source)
	if err != nil {
		return Retrieval{}, err
	}
	return splitTopK(hits, k), nil
}

// retrieveAcross is the shared body of QueryAcross and ExplainAcross: it embeds
// the query once, searches every (deduplicated) collection, tags each match with
// its origin, merges by score, then hydrates and splits into the top-k and the
// runner-up. All collections must share an embedding space.
func (q *Querier) retrieveAcross(ctx context.Context, names []string, query string, k int, source string, withRunnerUp bool) (Retrieval, error) {
	if strings.TrimSpace(query) == "" {
		return Retrieval{}, fmt.Errorf("query: %w: text must not be empty", domain.ErrInvalidArgument)
	}

	colls, err := q.resolveCollections(ctx, names)
	if err != nil {
		return Retrieval{}, err
	}
	if err := domain.SameSpace(colls); err != nil {
		return Retrieval{}, err
	}

	// All collections share a space (SameSpace passed), so checking the embedder
	// against the first covers them all — before any search runs.
	vec, err := q.embedQuery(ctx, query, colls[0])
	if err != nil {
		return Retrieval{}, err
	}

	searchK := searchBudget(k, source, withRunnerUp)
	var all []domain.VectorMatch
	collByID := make(map[domain.ChunkID]string)
	for _, c := range colls {
		matches, err := q.index.Search(ctx, c.Name, vec, searchK)
		if err != nil {
			return Retrieval{}, fmt.Errorf("search %q: %w", c.Name, err)
		}
		for _, m := range matches {
			collByID[m.ChunkID] = c.Name
			all = append(all, m)
		}
	}
	if len(all) == 0 {
		return Retrieval{}, nil
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].ChunkID < all[j].ChunkID // deterministic ties
	})

	hits, err := q.hydrate(ctx, all, collByID, source)
	if err != nil {
		return Retrieval{}, err
	}
	return splitTopK(hits, k), nil
}

// embedQuery enforces space coherence (invariant 1) then embeds the query in the
// collection's space, returning the single query vector. It fails with
// ErrSpaceMismatch before embedding if the embedder's space differs.
func (q *Querier) embedQuery(ctx context.Context, query string, coll *domain.Collection) ([]float32, error) {
	space, err := q.embedder.Space(ctx)
	if err != nil {
		return nil, fmt.Errorf("embedder space: %w", err)
	}
	if err := coll.AcceptsSpace(space); err != nil {
		return nil, err
	}
	vecs, err := q.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for 1 query", len(vecs))
	}
	return vecs[0], nil
}

// searchBudget is the number of candidates to fetch from the index for a given
// k: over-fetch when a source filter will thin the results, one extra for the
// runner-up under --explain, otherwise exactly k.
func searchBudget(k int, source string, withRunnerUp bool) int {
	switch {
	case source != "" && k > 0:
		return k * sourceOverfetch // over-fetch already exposes the runner-up
	case withRunnerUp && k > 0:
		return k + 1 // one extra candidate to surface the runner-up
	default:
		return k
	}
}

// splitTopK splits score-ordered hits into the returned top-k and the runner-up
// just beyond it (k <= 0 returns everything, no runner-up).
func splitTopK(hits []domain.ChunkHit, k int) Retrieval {
	r := Retrieval{Hits: hits}
	if k > 0 && len(hits) > k {
		r.NextScore = hits[k].Score
		r.HasNext = true
		r.Hits = hits[:k]
	}
	return r
}

// resolveCollections loads each named collection in order, skipping duplicates
// (so repeated -c flags are harmless). An unknown name is ErrNotFound; an empty
// list is ErrInvalidArgument.
func (q *Querier) resolveCollections(ctx context.Context, names []string) ([]*domain.Collection, error) {
	seen := make(map[string]bool, len(names))
	out := make([]*domain.Collection, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		c, err := q.collections.Get(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("query: %w: at least one collection is required", domain.ErrInvalidArgument)
	}
	return out, nil
}

// hydrate turns score-ordered vector matches into ChunkHits: it loads the
// chunks, attaches each chunk's source URI (and origin collection from collByID,
// for cross-collection results), preserves the match order, and drops hits whose
// source does not match a non-empty glob. matches must already be ordered;
// hydrate neither sorts nor truncates. collByID may be nil (single collection).
func (q *Querier) hydrate(ctx context.Context, matches []domain.VectorMatch, collByID map[domain.ChunkID]string, source string) ([]domain.ChunkHit, error) {
	ids := make([]domain.ChunkID, len(matches))
	scoreByID := make(map[domain.ChunkID]float64, len(matches))
	for i, m := range matches {
		ids[i] = m.ChunkID
		scoreByID[m.ChunkID] = m.Score
	}

	chunks, err := q.docs.GetChunks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate chunks: %w", err)
	}

	sourceByDoc, err := q.sources(ctx, chunks)
	if err != nil {
		return nil, err
	}

	hits := make([]domain.ChunkHit, 0, len(chunks))
	for _, c := range chunks {
		if source != "" && !matchSource(source, sourceByDoc[c.DocumentID]) {
			continue
		}
		hits = append(hits, domain.ChunkHit{
			Chunk:      c,
			Score:      scoreByID[c.ID],
			Source:     sourceByDoc[c.DocumentID],
			Collection: collByID[c.ID],
		})
	}
	return hits, nil
}

// matchSource reports whether a source URI matches the user's glob. A pattern
// without "/" matches the document basename (so "*.pdf" or "SSP*" work as
// expected); a pattern containing "/" matches the URI's path. A malformed
// pattern matches nothing.
func matchSource(pattern, sourceURI string) bool {
	p := sourceURI
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	candidate := p
	if !strings.Contains(pattern, "/") {
		candidate = path.Base(p)
	}
	ok, err := path.Match(pattern, candidate)
	return err == nil && ok
}

// sources hydrates the source URI of each chunk's document, returning a map from
// document ID to source URI. Documents that can't be hydrated are simply absent,
// leaving those hits with an empty Source rather than failing the whole query.
func (q *Querier) sources(ctx context.Context, chunks []domain.Chunk) (map[domain.DocumentID]string, error) {
	ids := make([]domain.DocumentID, 0, len(chunks))
	seen := make(map[domain.DocumentID]bool, len(chunks))
	for _, c := range chunks {
		if !seen[c.DocumentID] {
			seen[c.DocumentID] = true
			ids = append(ids, c.DocumentID)
		}
	}

	docs, err := q.docs.GetDocuments(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate sources: %w", err)
	}
	byDoc := make(map[domain.DocumentID]string, len(docs))
	for _, d := range docs {
		byDoc[d.ID] = d.SourceURI
	}
	return byDoc, nil
}
