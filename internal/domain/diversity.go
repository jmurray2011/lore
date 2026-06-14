package domain

import "math"

// CapPerSource caps the number of hits kept per source document to max,
// preserving the input order (assumed already ranked). A document that dominates
// the ranking cannot then sweep the result set. max <= 0 is a no-op.
func CapPerSource(hits []ChunkHit, max int) []ChunkHit {
	if max <= 0 {
		return hits
	}
	counts := make(map[string]int, len(hits))
	out := make([]ChunkHit, 0, len(hits))
	for _, h := range hits {
		if counts[h.Source] >= max {
			continue
		}
		counts[h.Source]++
		out = append(out, h)
	}
	return out
}

// MMRCandidate is a hit paired with its embedding vector, the input to Maximal
// Marginal Relevance selection. The hit's Score is used as its relevance to the
// query; the vector is used for the redundancy penalty against already-selected
// hits.
type MMRCandidate struct {
	Hit    ChunkHit
	Vector []float32
}

// SelectMMR reorders candidates by Maximal Marginal Relevance, greedily picking
// the candidate that maximizes lambda*relevance - (1-lambda)*maxSimilarityToSelected,
// where relevance is the hit's (cosine) Score and similarity is cosine between
// candidate vectors. lambda in [0,1] trades relevance (1.0) against diversity
// (0.0); ~0.5 is a balanced default. It returns up to k selected hits in
// selection order; k <= 0 selects all. Ties break by ChunkID for determinism.
//
// Relevance (Score) and the similarity penalty are both cosine-scaled, so they
// combine meaningfully; under --rerank the rerank score is a different scale and
// is deliberately not used here (cosine relevance keeps MMR's two terms comparable).
func SelectMMR(candidates []MMRCandidate, lambda float64, k int) []ChunkHit {
	n := len(candidates)
	if k <= 0 || k > n {
		k = n
	}
	if n == 0 {
		return nil
	}

	remaining := make([]MMRCandidate, n)
	copy(remaining, candidates)
	selected := make([]ChunkHit, 0, k)
	var selectedVecs [][]float32

	for len(selected) < k && len(remaining) > 0 {
		bestIdx := -1
		var bestScore float64
		for i, c := range remaining {
			var maxSim float64
			for _, sv := range selectedVecs {
				if s := cosineSim(c.Vector, sv); s > maxSim {
					maxSim = s
				}
			}
			score := lambda*c.Hit.Score - (1-lambda)*maxSim
			if bestIdx == -1 || score > bestScore ||
				(score == bestScore && c.Hit.Chunk.ID < remaining[bestIdx].Hit.Chunk.ID) {
				bestIdx, bestScore = i, score
			}
		}
		chosen := remaining[bestIdx]
		selected = append(selected, chosen.Hit)
		selectedVecs = append(selectedVecs, chosen.Vector)
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return selected
}

// cosineSim is the cosine similarity of two vectors, 0 for degenerate input
// (mismatched lengths or a zero vector). It matches the adapters' cosine so MMR's
// redundancy penalty is on the same scale as retrieval similarity.
func cosineSim(a, b []float32) float64 {
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
