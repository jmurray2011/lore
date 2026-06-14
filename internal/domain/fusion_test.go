package domain_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestFuseRRF(t *testing.T) {
	t.Run("a chunk ranked in both lists outranks one in a single list", func(t *testing.T) {
		vector := []domain.ChunkID{"a", "b", "c"}
		lexical := []domain.ChunkID{"b", "x", "y"}
		got := domain.FuseRRF(domain.RRFDefaultK, vector, lexical)

		order := make([]domain.ChunkID, len(got))
		for i, r := range got {
			order[i] = r.ID
		}
		// b appears in both lists → highest. Then a (vector #1) beats x (lexical #2),
		// and c/y tie at rank 3 of their single list, broken by ID (c < y).
		want := []domain.ChunkID{"b", "a", "x", "c", "y"}
		if len(order) != len(want) {
			t.Fatalf("got %v, want %v", order, want)
		}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("fused order = %v, want %v", order, want)
			}
		}
		// Scores are non-increasing.
		for i := 1; i < len(got); i++ {
			if got[i].Score > got[i-1].Score {
				t.Errorf("scores must be non-increasing at %d: %v", i, got)
			}
		}
	})

	t.Run("a single list preserves its order", func(t *testing.T) {
		got := domain.FuseRRF(domain.RRFDefaultK, []domain.ChunkID{"a", "b", "c"})
		want := []domain.ChunkID{"a", "b", "c"}
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("no lists or only empty lists yield nothing", func(t *testing.T) {
		if got := domain.FuseRRF(domain.RRFDefaultK); len(got) != 0 {
			t.Errorf("no lists: want empty, got %v", got)
		}
		if got := domain.FuseRRF(domain.RRFDefaultK, nil, []domain.ChunkID{}); len(got) != 0 {
			t.Errorf("empty lists: want empty, got %v", got)
		}
	})

	t.Run("a duplicate within one list counts only at its best rank", func(t *testing.T) {
		// "a" twice in one list must not be double-weighted past "b".
		one := domain.FuseRRF(domain.RRFDefaultK, []domain.ChunkID{"a", "a", "b"})
		two := domain.FuseRRF(domain.RRFDefaultK, []domain.ChunkID{"a", "b"})
		if len(one) != 2 || one[0].ID != "a" || one[1].ID != "b" {
			t.Fatalf("duplicate handling: got %v", one)
		}
		if one[0].Score != two[0].Score || one[1].Score != two[1].Score {
			t.Errorf("a duplicate must not change scores: %v vs %v", one, two)
		}
	})

	t.Run("k dampens the rank weighting", func(t *testing.T) {
		// With a larger k, the gap between rank 1 and rank 2 shrinks.
		small := domain.FuseRRF(1, []domain.ChunkID{"a", "b"})
		large := domain.FuseRRF(1000, []domain.ChunkID{"a", "b"})
		gapSmall := small[0].Score - small[1].Score
		gapLarge := large[0].Score - large[1].Score
		if gapLarge >= gapSmall {
			t.Errorf("larger k should dampen the rank-1/rank-2 gap: small=%v large=%v", gapSmall, gapLarge)
		}
	})
}
