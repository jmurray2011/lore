package domain_test

import (
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestHitTime(t *testing.T) {
	ingested := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mkHit := func(meta domain.Metadata) domain.ChunkHit {
		return domain.ChunkHit{Metadata: meta, IngestedAt: ingested}
	}

	t.Run("prefers updated over created and ingested", func(t *testing.T) {
		h := mkHit(domain.Metadata{"updated": "2026-06-01", "created": "2025-01-01"})
		got, ok := domain.HitTime(h)
		if !ok || !got.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("HitTime = %v, %v; want 2026-06-01", got, ok)
		}
	})

	t.Run("falls back to created when no updated", func(t *testing.T) {
		h := mkHit(domain.Metadata{"created": "2025-03-04"})
		got, ok := domain.HitTime(h)
		if !ok || !got.Equal(time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("HitTime = %v, %v; want 2025-03-04", got, ok)
		}
	})

	t.Run("falls back to IngestedAt when no date metadata", func(t *testing.T) {
		got, ok := domain.HitTime(mkHit(nil))
		if !ok || !got.Equal(ingested) {
			t.Fatalf("HitTime = %v, %v; want IngestedAt", got, ok)
		}
	})

	t.Run("parses RFC3339", func(t *testing.T) {
		h := mkHit(domain.Metadata{"updated": "2026-06-01T12:30:00Z"})
		got, ok := domain.HitTime(h)
		if !ok || !got.Equal(time.Date(2026, 6, 1, 12, 30, 0, 0, time.UTC)) {
			t.Fatalf("HitTime = %v, %v", got, ok)
		}
	})

	t.Run("unknown when no metadata and zero IngestedAt", func(t *testing.T) {
		if _, ok := domain.HitTime(domain.ChunkHit{}); ok {
			t.Error("want unknown (ok=false) for an undated hit with zero IngestedAt")
		}
	})

	t.Run("unparseable date falls through to IngestedAt", func(t *testing.T) {
		got, ok := domain.HitTime(mkHit(domain.Metadata{"updated": "last tuesday"}))
		if !ok || !got.Equal(ingested) {
			t.Fatalf("HitTime = %v, %v; want IngestedAt fallback", got, ok)
		}
	})
}

func TestDecayByRecency(t *testing.T) {
	now := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	halfLife := 90 * 24 * time.Hour

	hit := func(id string, score float64, meta domain.Metadata) domain.ChunkHit {
		return domain.ChunkHit{Chunk: domain.Chunk{ID: domain.ChunkID(id)}, Score: score, Metadata: meta}
	}

	t.Run("a fresher chunk overtakes a slightly-more-relevant stale one", func(t *testing.T) {
		stale := hit("stale", 0.80, domain.Metadata{"updated": "2025-01-01"}) // ~1.4 yr old
		fresh := hit("fresh", 0.70, domain.Metadata{"updated": "2026-06-10"}) // 4 days old
		got := domain.DecayByRecency([]domain.ChunkHit{stale, fresh}, halfLife, now, 0)
		if len(got) != 2 || got[0].Chunk.ID != "fresh" {
			t.Fatalf("want fresh first after decay, got order %v", ids(got))
		}
		if got[0].Score != 0.70 {
			t.Errorf("Score should stay cosine (0.70), got %v", got[0].Score)
		}
	})

	t.Run("relevance still wins for a much-more-relevant, only-mildly-older chunk", func(t *testing.T) {
		// ~30 days old (a third of a half-life → decay ~0.79): 0.95*0.79 ≈ 0.75
		strong := hit("strong", 0.95, domain.Metadata{"updated": "2026-05-15"})
		fresh := hit("fresh", 0.40, domain.Metadata{"updated": "2026-06-13"}) // decay ~1.0 → 0.40
		got := domain.DecayByRecency([]domain.ChunkHit{fresh, strong}, halfLife, now, 0)
		if got[0].Chunk.ID != "strong" {
			t.Fatalf("a much-more-relevant, only-mildly-older chunk should still lead, got %v", ids(got))
		}
	})

	t.Run("undated hits keep full score (not buried)", func(t *testing.T) {
		undated := hit("undated", 0.75, nil)
		old := hit("old", 0.80, domain.Metadata{"updated": "2024-01-01"})
		got := domain.DecayByRecency([]domain.ChunkHit{old, undated}, halfLife, now, 0)
		if got[0].Chunk.ID != "undated" {
			t.Fatalf("undated (decay 1.0, 0.75) should beat heavily-decayed old (0.80), got %v", ids(got))
		}
	})

	t.Run("future-dated hit keeps full score (no negative-age boost)", func(t *testing.T) {
		future := hit("future", 0.50, domain.Metadata{"updated": "2099-01-01"})
		recent := hit("recent", 0.55, domain.Metadata{"updated": "2026-06-14"})
		got := domain.DecayByRecency([]domain.ChunkHit{future, recent}, halfLife, now, 0)
		if got[0].Chunk.ID != "recent" {
			t.Fatalf("future date must not boost; want relevance order, got %v", ids(got))
		}
	})

	t.Run("halfLife <= 0 is a no-op (order unchanged)", func(t *testing.T) {
		a := hit("a", 0.40, domain.Metadata{"updated": "2026-06-13"})
		b := hit("b", 0.90, domain.Metadata{"updated": "2020-01-01"})
		got := domain.DecayByRecency([]domain.ChunkHit{a, b}, 0, now, 0)
		if got[0].Chunk.ID != "a" || got[1].Chunk.ID != "b" {
			t.Fatalf("halfLife<=0 must not reorder, got %v", ids(got))
		}
	})

	t.Run("trims to k after reordering", func(t *testing.T) {
		hits := []domain.ChunkHit{
			hit("old", 0.90, domain.Metadata{"updated": "2024-01-01"}),
			hit("mid", 0.70, domain.Metadata{"updated": "2026-01-01"}),
			hit("new", 0.60, domain.Metadata{"updated": "2026-06-13"}),
		}
		got := domain.DecayByRecency(hits, halfLife, now, 2)
		if len(got) != 2 {
			t.Fatalf("want top-2 after trim, got %d", len(got))
		}
		if got[0].Chunk.ID != "new" {
			t.Errorf("freshest should lead, got %v", ids(got))
		}
	})
}

func ids(hits []domain.ChunkHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = string(h.Chunk.ID)
	}
	return out
}
