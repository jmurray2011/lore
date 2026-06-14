package domain_test

import (
	"math"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRetrievalMetrics(t *testing.T) {
	retrieved := []string{"a", "x", "b", "y"} // ranked, best first
	relevant := map[string]bool{"a": true, "b": true, "c": true}

	t.Run("recall@k", func(t *testing.T) {
		if got := domain.RecallAtK(retrieved, relevant, 4); !approx(got, 2.0/3.0) {
			t.Errorf("recall@4 = %v, want 2/3", got)
		}
		if got := domain.RecallAtK(retrieved, relevant, 2); !approx(got, 1.0/3.0) {
			t.Errorf("recall@2 = %v, want 1/3 (only 'a' in top 2)", got)
		}
		if got := domain.RecallAtK(retrieved, map[string]bool{}, 4); got != 0 {
			t.Errorf("recall with no relevant set = %v, want 0", got)
		}
	})

	t.Run("precision@k", func(t *testing.T) {
		if got := domain.PrecisionAtK(retrieved, relevant, 4); !approx(got, 0.5) {
			t.Errorf("precision@4 = %v, want 0.5", got)
		}
		if got := domain.PrecisionAtK(retrieved, relevant, 2); !approx(got, 0.5) {
			t.Errorf("precision@2 = %v, want 0.5 (a hit, x miss)", got)
		}
	})

	t.Run("MRR", func(t *testing.T) {
		if got := domain.MRR(retrieved, relevant); !approx(got, 1.0) {
			t.Errorf("MRR = %v, want 1 (first relevant at rank 1)", got)
		}
		if got := domain.MRR([]string{"x", "y", "b"}, relevant); !approx(got, 1.0/3.0) {
			t.Errorf("MRR = %v, want 1/3 (first relevant at rank 3)", got)
		}
		if got := domain.MRR([]string{"x", "y"}, relevant); got != 0 {
			t.Errorf("MRR with no relevant retrieved = %v, want 0", got)
		}
	})

	t.Run("hit rate", func(t *testing.T) {
		if got := domain.HitRate(retrieved, relevant, 4); got != 1 {
			t.Errorf("hit@4 = %v, want 1", got)
		}
		if got := domain.HitRate([]string{"x", "y"}, relevant, 2); got != 0 {
			t.Errorf("hit@2 = %v, want 0", got)
		}
	})

	t.Run("nDCG@k", func(t *testing.T) {
		// rel at ranks 1 and 3: DCG = 1/log2(2) + 1/log2(4) = 1.5.
		// IDCG (3 relevant, ideal ranks 1,2,3) = 1 + 1/log2(3) + 1/log2(4).
		idcg := 1 + 1/math.Log2(3) + 1/math.Log2(4)
		want := 1.5 / idcg
		if got := domain.NDCGAtK(retrieved, relevant, 4); !approx(got, want) {
			t.Errorf("nDCG@4 = %v, want %v", got, want)
		}
		if got := domain.NDCGAtK(retrieved, map[string]bool{}, 4); got != 0 {
			t.Errorf("nDCG with no relevant = %v, want 0", got)
		}
	})
}
