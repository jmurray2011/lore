package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/adapters/cache"
	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// countingGen records how many times it was asked to synthesize, so a hit can be
// proven by the inner generator NOT being called.
type countingGen struct {
	calls  int
	answer app.Answer
}

func (g *countingGen) Synthesize(_ context.Context, _ string, _ []domain.ChunkHit, _ []domain.Attachment) (app.Answer, error) {
	g.calls++
	return g.answer, nil
}

func TestCachingGenerator(t *testing.T) {
	ctx := context.Background()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	chunk := func(t *testing.T, text string) domain.ChunkHit {
		t.Helper()
		c, err := domain.NewChunk(docID, 0, text)
		if err != nil {
			t.Fatal(err)
		}
		return domain.ChunkHit{Chunk: c, Source: "file:///a.md", Score: 0.9}
	}

	// A fixed, advanceable clock so TTL behavior is deterministic.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	newGen := func(inner app.Generator, store app.AnswerCache, salt string, ttl time.Duration) app.Generator {
		return cache.NewGenerator(inner, store, salt, ttl, clock)
	}

	t.Run("caches a miss then serves the hit without calling the inner generator", func(t *testing.T) {
		inner := &countingGen{answer: app.Answer{Text: "cached answer", Citations: []domain.Citation{{ChunkID: domain.DeriveChunkID(docID, 0), Source: "file:///a.md", Seq: 0}}}}
		g := newGen(inner, memstore.NewAnswerCache(), "salt", time.Hour)
		hits := []domain.ChunkHit{chunk(t, "alpha text")}

		first, err := g.Synthesize(ctx, "why?", hits, nil)
		if err != nil {
			t.Fatalf("first Synthesize: %v", err)
		}
		second, err := g.Synthesize(ctx, "why?", hits, nil)
		if err != nil {
			t.Fatalf("second Synthesize: %v", err)
		}
		if inner.calls != 1 {
			t.Errorf("inner generator should be called once (the miss), got %d", inner.calls)
		}
		if first.Text != "cached answer" || second.Text != first.Text {
			t.Errorf("hit must return the cached answer: %q then %q", first.Text, second.Text)
		}
		if len(second.Citations) != 1 || second.Citations[0].ChunkID != domain.DeriveChunkID(docID, 0) {
			t.Errorf("citations must round-trip through the cache: %+v", second.Citations)
		}
	})

	t.Run("a different question is a different key (miss)", func(t *testing.T) {
		inner := &countingGen{answer: app.Answer{Text: "x"}}
		g := newGen(inner, memstore.NewAnswerCache(), "salt", time.Hour)
		hits := []domain.ChunkHit{chunk(t, "alpha text")}
		_, _ = g.Synthesize(ctx, "first?", hits, nil)
		_, _ = g.Synthesize(ctx, "second?", hits, nil)
		if inner.calls != 2 {
			t.Errorf("distinct questions must each miss, got %d calls", inner.calls)
		}
	})

	t.Run("same chunk ID but changed text is a different key (content-edit invalidation)", func(t *testing.T) {
		inner := &countingGen{answer: app.Answer{Text: "x"}}
		g := newGen(inner, memstore.NewAnswerCache(), "salt", time.Hour)
		// Both hits share the chunk ID docID:0 (IDs are path-stable), but the
		// text differs as if the source document were edited and re-ingested.
		_, _ = g.Synthesize(ctx, "why?", []domain.ChunkHit{chunk(t, "original text")}, nil)
		_, _ = g.Synthesize(ctx, "why?", []domain.ChunkHit{chunk(t, "edited text")}, nil)
		if inner.calls != 2 {
			t.Errorf("changed grounding text must invalidate the key, got %d calls", inner.calls)
		}
	})

	t.Run("requests with attachments are never cached", func(t *testing.T) {
		inner := &countingGen{answer: app.Answer{Text: "x"}}
		g := newGen(inner, memstore.NewAnswerCache(), "salt", time.Hour)
		att, err := domain.NewAttachment("image/png", "c.png", []byte{1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		hits := []domain.ChunkHit{chunk(t, "alpha text")}
		_, _ = g.Synthesize(ctx, "why?", hits, []domain.Attachment{att})
		_, _ = g.Synthesize(ctx, "why?", hits, []domain.Attachment{att})
		if inner.calls != 2 {
			t.Errorf("attachment-bearing requests must bypass the cache, got %d calls", inner.calls)
		}
	})

	t.Run("an entry older than the TTL is a miss", func(t *testing.T) {
		inner := &countingGen{answer: app.Answer{Text: "x"}}
		store := memstore.NewAnswerCache()
		g := newGen(inner, store, "salt", time.Hour)
		hits := []domain.ChunkHit{chunk(t, "alpha text")}

		_, _ = g.Synthesize(ctx, "why?", hits, nil) // stored at noon
		now = now.Add(2 * time.Hour)                // clock past the 1h TTL
		_, _ = g.Synthesize(ctx, "why?", hits, nil) // expired → miss → inner again
		if inner.calls != 2 {
			t.Errorf("an expired entry must miss, got %d calls", inner.calls)
		}
		now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) // reset for other subtests
	})

	t.Run("the salt separates entries (model/prompt change)", func(t *testing.T) {
		store := memstore.NewAnswerCache() // shared store
		hits := []domain.ChunkHit{chunk(t, "alpha text")}

		a := &countingGen{answer: app.Answer{Text: "a"}}
		b := &countingGen{answer: app.Answer{Text: "b"}}
		_, _ = newGen(a, store, "model-v1", time.Hour).Synthesize(ctx, "why?", hits, nil)
		_, _ = newGen(b, store, "model-v2", time.Hour).Synthesize(ctx, "why?", hits, nil)
		if a.calls != 1 || b.calls != 1 {
			t.Errorf("a different salt must not hit another salt's entry: a=%d b=%d", a.calls, b.calls)
		}
	})
}
