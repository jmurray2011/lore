// Package memstore provides in-memory reference implementations of the
// persistence ports. It defines the reference semantics every other engine
// must match (via the internal/conformance suites) and is a perfectly
// serviceable backend for small corpora: brute-force cosine over float32
// slices handles ~100k chunks in single-digit milliseconds.
package memstore

import (
	"context"
	"math"
	"sort"
	"sync"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// VectorIndex is a thread-safe, brute-force cosine similarity index.
type VectorIndex struct {
	mu          sync.RWMutex
	collections map[string]map[domain.ChunkID][]float32
}

// compile-time port check
var _ app.VectorIndex = (*VectorIndex)(nil)

// NewVectorIndex returns an empty index.
func NewVectorIndex() *VectorIndex {
	return &VectorIndex{collections: make(map[string]map[domain.ChunkID][]float32)}
}

// Upsert stores copies of the vectors, replacing entries with the same ChunkID.
func (x *VectorIndex) Upsert(_ context.Context, collection string, entries []app.VectorEntry) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	col, ok := x.collections[collection]
	if !ok {
		col = make(map[domain.ChunkID][]float32, len(entries))
		x.collections[collection] = col
	}
	for _, e := range entries {
		v := make([]float32, len(e.Vector))
		copy(v, e.Vector)
		col[e.ChunkID] = v
	}
	return nil
}

// Search returns up to k matches by cosine similarity, best first.
func (x *VectorIndex) Search(_ context.Context, collection string, query []float32, k int) ([]domain.VectorMatch, error) {
	if k <= 0 {
		return nil, nil
	}

	x.mu.RLock()
	defer x.mu.RUnlock()

	col := x.collections[collection]
	matches := make([]domain.VectorMatch, 0, len(col))
	for id, vec := range col {
		matches = append(matches, domain.VectorMatch{ChunkID: id, Score: cosine(query, vec)})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].ChunkID < matches[j].ChunkID // deterministic ties
	})
	if len(matches) > k {
		matches = matches[:k]
	}
	return matches, nil
}

// Entries returns a copy of every stored vector for the collection (order
// unspecified). An unknown collection yields no entries and no error.
func (x *VectorIndex) Entries(_ context.Context, collection string) ([]app.VectorEntry, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()

	col := x.collections[collection]
	out := make([]app.VectorEntry, 0, len(col))
	for id, vec := range col {
		v := make([]float32, len(vec))
		copy(v, vec)
		out = append(out, app.VectorEntry{ChunkID: id, Vector: v})
	}
	return out, nil
}

// Delete removes the given chunk IDs; absent IDs are a no-op.
func (x *VectorIndex) Delete(_ context.Context, collection string, ids []domain.ChunkID) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	col := x.collections[collection]
	for _, id := range ids {
		delete(col, id)
	}
	return nil
}

// cosine returns the cosine similarity of a and b, 0 for degenerate input
// (mismatched lengths or zero vectors).
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
