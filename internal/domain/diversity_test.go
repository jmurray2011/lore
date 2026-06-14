package domain_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func hit(id domain.ChunkID, source string, score float64) domain.ChunkHit {
	return domain.ChunkHit{Chunk: domain.Chunk{ID: id}, Source: source, Score: score}
}

func TestCapPerSource(t *testing.T) {
	t.Run("keeps at most max hits per source, preserving order", func(t *testing.T) {
		hits := []domain.ChunkHit{
			hit("1", "a.md", 0.9),
			hit("2", "a.md", 0.8),
			hit("3", "b.md", 0.7),
			hit("4", "a.md", 0.6), // 3rd from a.md — dropped at max=2
			hit("5", "c.md", 0.5),
		}
		got := domain.CapPerSource(hits, 2)
		var ids []domain.ChunkID
		for _, h := range got {
			ids = append(ids, h.Chunk.ID)
		}
		want := []domain.ChunkID{"1", "2", "3", "5"}
		if len(ids) != len(want) {
			t.Fatalf("got %v, want %v", ids, want)
		}
		for i := range want {
			if ids[i] != want[i] {
				t.Fatalf("got %v, want %v", ids, want)
			}
		}
	})

	t.Run("max <= 0 is a no-op", func(t *testing.T) {
		hits := []domain.ChunkHit{hit("1", "a.md", 0.9), hit("2", "a.md", 0.8)}
		if got := domain.CapPerSource(hits, 0); len(got) != 2 {
			t.Errorf("max=0 should not cap, got %d", len(got))
		}
	})
}

func TestSelectMMR(t *testing.T) {
	// A and B are near-duplicates (same vector); C is orthogonal but lower relevance.
	cands := []domain.MMRCandidate{
		{Hit: hit("A", "x", 0.9), Vector: []float32{1, 0}},
		{Hit: hit("B", "y", 0.85), Vector: []float32{1, 0}},
		{Hit: hit("C", "z", 0.7), Vector: []float32{0, 1}},
	}

	t.Run("balanced lambda demotes the redundant candidate", func(t *testing.T) {
		got := domain.SelectMMR(cands, 0.5, 3)
		var ids []domain.ChunkID
		for _, h := range got {
			ids = append(ids, h.Chunk.ID)
		}
		// A first (highest relevance); then C (diverse) outranks B (redundant with A).
		want := []domain.ChunkID{"A", "C", "B"}
		for i := range want {
			if ids[i] != want[i] {
				t.Fatalf("MMR order = %v, want %v", ids, want)
			}
		}
	})

	t.Run("lambda 1 is pure relevance order", func(t *testing.T) {
		got := domain.SelectMMR(cands, 1, 3)
		if got[0].Chunk.ID != "A" || got[1].Chunk.ID != "B" || got[2].Chunk.ID != "C" {
			t.Errorf("lambda=1 should rank by relevance A,B,C, got %v", got)
		}
	})

	t.Run("k limits the selection", func(t *testing.T) {
		if got := domain.SelectMMR(cands, 0.5, 2); len(got) != 2 {
			t.Errorf("k=2 should select 2, got %d", len(got))
		}
	})
}
