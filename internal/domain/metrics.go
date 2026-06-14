package domain

import "math"

// Retrieval-quality metrics over a ranked list of identifiers (chunk IDs or source
// URIs, best first) against a set of relevant ones. They are pure functions so the
// eval harness can report them deterministically; binary relevance throughout.
// k <= 0 means "consider the whole list".

// topK returns the first k of retrieved (all of it when k <= 0 or k >= len).
func topK(retrieved []string, k int) []string {
	if k <= 0 || k >= len(retrieved) {
		return retrieved
	}
	return retrieved[:k]
}

// RecallAtK is the fraction of the relevant set found in the top k. 0 when there
// is nothing relevant (recall is undefined; callers exclude such cases).
func RecallAtK(retrieved []string, relevant map[string]bool, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	hits := 0
	for _, id := range topK(retrieved, k) {
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// PrecisionAtK is the fraction of the top k that is relevant. 0 when nothing was
// retrieved.
func PrecisionAtK(retrieved []string, relevant map[string]bool, k int) float64 {
	top := topK(retrieved, k)
	if len(top) == 0 {
		return 0
	}
	hits := 0
	for _, id := range top {
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(top))
}

// MRR is the reciprocal rank of the first relevant identifier (1-based), or 0 if
// none is relevant.
func MRR(retrieved []string, relevant map[string]bool) float64 {
	for i, id := range retrieved {
		if relevant[id] {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// HitRate is 1 if any of the top k is relevant, else 0.
func HitRate(retrieved []string, relevant map[string]bool, k int) float64 {
	for _, id := range topK(retrieved, k) {
		if relevant[id] {
			return 1
		}
	}
	return 0
}

// NDCGAtK is the normalized discounted cumulative gain at k with binary relevance:
// DCG over the top k divided by the ideal DCG (all relevant ranked first). 0 when
// there is nothing relevant.
func NDCGAtK(retrieved []string, relevant map[string]bool, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	top := topK(retrieved, k)
	var dcg float64
	for i, id := range top {
		if relevant[id] {
			dcg += 1 / math.Log2(float64(i+2)) // rank i (0-based) → log2(rank+1)=log2(i+2)
		}
	}
	ideal := len(relevant)
	if k > 0 && k < ideal {
		ideal = k
	}
	var idcg float64
	for i := 0; i < ideal; i++ {
		idcg += 1 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}
