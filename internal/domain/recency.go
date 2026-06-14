package domain

import (
	"math"
	"sort"
	"time"
)

// recencyLayouts are the timestamp formats accepted in document metadata dates,
// most specific first.
var recencyLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"}

// HitTime returns the most meaningful timestamp for ranking a hit by recency —
// its document's "updated" metadata, then "created", then IngestedAt — and
// whether one was found. A zero IngestedAt counts as unknown (ok=false) so an
// undated hit is treated as neutral rather than ancient.
func HitTime(h ChunkHit) (time.Time, bool) {
	for _, key := range []string{"updated", "created"} {
		if v := h.Metadata[key]; v != "" {
			if t, ok := parseMetaTime(v); ok {
				return t, true
			}
		}
	}
	if !h.IngestedAt.IsZero() {
		return h.IngestedAt, true
	}
	return time.Time{}, false
}

// parseMetaTime parses a metadata date in any accepted layout.
func parseMetaTime(v string) (time.Time, bool) {
	for _, layout := range recencyLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// DecayByRecency reorders hits by relevance blended with an exponential time
// decay, then returns the top k (all of them when k <= 0). Each hit's cosine
// Score is multiplied by 2^(-age/halfLife), where age is now minus the hit's
// timestamp (HitTime); the hits are stable-sorted by this adjusted score. A hit
// with no known timestamp, or one dated in the future, keeps its full score
// (decay 1.0) — only known-older hits are demoted, so retrieval never buries an
// undated chunk on a guess. The cosine Score is preserved for display; only the
// order changes (mirroring rerank/MMR). halfLife <= 0 is a no-op reorder.
func DecayByRecency(hits []ChunkHit, halfLife time.Duration, now time.Time, k int) []ChunkHit {
	if halfLife > 0 && len(hits) > 1 {
		adjusted := make([]float64, len(hits))
		for i, h := range hits {
			factor := 1.0
			if ts, ok := HitTime(h); ok {
				if age := now.Sub(ts); age > 0 {
					factor = math.Exp2(-float64(age) / float64(halfLife))
				}
			}
			adjusted[i] = h.Score * factor
		}
		order := make([]int, len(hits))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return adjusted[order[a]] > adjusted[order[b]] })
		sorted := make([]ChunkHit, len(hits))
		for i, j := range order {
			sorted[i] = hits[j]
		}
		hits = sorted
	}
	if k > 0 && k < len(hits) {
		hits = hits[:k]
	}
	return hits
}
