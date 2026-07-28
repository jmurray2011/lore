package domain

import "sort"

// RRFDefaultK is the conventional Reciprocal Rank Fusion constant. It dampens the
// weight of low ranks so a chunk near the top of several lists outranks one at the
// top of a single list without any one list dominating.
const RRFDefaultK = 60

// RankedID is a chunk identity with its fusion score, best (highest) first.
type RankedID struct {
	ID    ChunkID
	Score float64
}

// FuseRRF combines several ranked lists of chunk IDs by Reciprocal Rank Fusion:
// each list contributes 1/(k + rank) to an ID's score (rank is 1-based, best
// first), summed across the lists it appears in; the result is sorted by score
// descending, ties broken by ID for determinism. A duplicate ID within a single
// list counts only at its best (first) rank. Rank-based fusion needs no score
// normalization, which is why it is the default merge of the (cosine) vector and
// (BM25) lexical result lists — their score scales are not comparable, but their
// ranks are.
func FuseRRF(k int, lists ...[]ChunkID) []RankedID {
	scores := make(map[ChunkID]float64)
	for _, list := range lists {
		seen := make(map[ChunkID]bool, len(list))
		rank := 0
		for _, id := range list {
			if seen[id] {
				continue // count an ID once, at its best rank; duplicates compress
			}
			seen[id] = true
			rank++
			scores[id] += 1 / (float64(k) + float64(rank))
		}
	}

	out := make([]RankedID, 0, len(scores))
	for id, s := range scores {
		out = append(out, RankedID{ID: id, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID // deterministic ties
	})
	return out
}
