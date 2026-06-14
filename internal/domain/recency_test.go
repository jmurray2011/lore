package domain_test

import (
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestHitTime(t *testing.T) {
	date := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	t.Run("explicit modified front-matter wins over everything", func(t *testing.T) {
		h := domain.ChunkHit{
			Source:     "file:///logs/2024-01-01.md",
			Metadata:   domain.Metadata{"updated": "2026-06-01", "created": "2020-01-01", domain.MetaKeyModTime: "2025-01-01"},
			IngestedAt: date(2023, 1, 1),
		}
		if got, ok := domain.HitTime(h); !ok || !got.Equal(date(2026, 6, 1)) {
			t.Fatalf("HitTime = %v,%v; want updated 2026-06-01", got, ok)
		}
	})

	t.Run("modified synonyms match case-insensitively", func(t *testing.T) {
		for _, key := range []string{"Updated", "modified", "lastmod", "last_modified"} {
			h := domain.ChunkHit{Metadata: domain.Metadata{key: "2026-06-02"}}
			if got, ok := domain.HitTime(h); !ok || !got.Equal(date(2026, 6, 2)) {
				t.Errorf("key %q: HitTime = %v,%v; want 2026-06-02", key, got, ok)
			}
		}
	})

	t.Run("filesystem mtime beats filename and created", func(t *testing.T) {
		h := domain.ChunkHit{
			Source:   "file:///logs/2024-03-03.md",
			Metadata: domain.Metadata{domain.MetaKeyModTime: "2026-06-10T08:00:00Z", "created": "2020-01-01"},
		}
		if got, ok := domain.HitTime(h); !ok || !got.Equal(time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)) {
			t.Fatalf("HitTime = %v,%v; want mtime 2026-06-10T08:00", got, ok)
		}
	})

	t.Run("filename ISO date beats created when no modified/mtime", func(t *testing.T) {
		h := domain.ChunkHit{Source: "file:///Work-Log/daily/2026-06-09.md", Metadata: domain.Metadata{"created": "2020-01-01"}}
		if got, ok := domain.HitTime(h); !ok || !got.Equal(date(2026, 6, 9)) {
			t.Fatalf("HitTime = %v,%v; want filename 2026-06-09", got, ok)
		}
	})

	t.Run("filename ISO week resolves to that week's Monday", func(t *testing.T) {
		got, ok := domain.HitTime(domain.ChunkHit{Source: "file:///Work-Log/weekly/2026-W20.md"})
		if !ok {
			t.Fatal("want a date from the ISO-week filename")
		}
		y, w := got.ISOWeek()
		if y != 2026 || w != 20 || got.Weekday() != time.Monday {
			t.Fatalf("HitTime = %v (ISO %d-W%02d, %s); want Monday of 2026-W20", got, y, w, got.Weekday())
		}
	})

	t.Run("created/date front-matter used only after stronger signals", func(t *testing.T) {
		if got, ok := domain.HitTime(domain.ChunkHit{Metadata: domain.Metadata{"date": "2025-03-04"}}); !ok || !got.Equal(date(2025, 3, 4)) {
			t.Fatalf("HitTime = %v,%v; want created/date 2025-03-04", got, ok)
		}
	})

	t.Run("ingest time is the last resort", func(t *testing.T) {
		h := domain.ChunkHit{Source: "file:///notes/architecture.md", IngestedAt: date(2024, 1, 1)}
		if got, ok := domain.HitTime(h); !ok || !got.Equal(date(2024, 1, 1)) {
			t.Fatalf("HitTime = %v,%v; want IngestedAt", got, ok)
		}
	})

	t.Run("unknown when no signal at all", func(t *testing.T) {
		if _, ok := domain.HitTime(domain.ChunkHit{Source: "file:///notes/architecture.md"}); ok {
			t.Error("want unknown (ok=false) when no date signal exists")
		}
	})

	t.Run("an unparseable explicit date falls through to the next signal", func(t *testing.T) {
		h := domain.ChunkHit{Source: "file:///logs/2026-06-09.md", Metadata: domain.Metadata{"updated": "last tuesday"}}
		if got, ok := domain.HitTime(h); !ok || !got.Equal(date(2026, 6, 9)) {
			t.Fatalf("HitTime = %v,%v; want filename fallback 2026-06-09", got, ok)
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
