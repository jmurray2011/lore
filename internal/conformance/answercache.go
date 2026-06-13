package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// RunAnswerCacheSuite verifies the app.AnswerCache contract. The factory must
// return a fresh, empty cache per call. Times are passed explicitly (the store
// holds no clock), so these cases are deterministic.
func RunAnswerCacheSuite(t *testing.T, factory func(t *testing.T) app.AnswerCache) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	answer := func(text string, ids ...domain.ChunkID) app.Answer {
		cites := make([]domain.Citation, len(ids))
		for i, id := range ids {
			cites[i] = domain.Citation{ChunkID: id, Source: "file:///a.md", Seq: i}
		}
		return app.Answer{Text: text, Citations: cites}
	}

	t.Run("put then get round-trips the answer and its citations", func(t *testing.T) {
		c := factory(t)
		want := answer("the grounded answer", "deadbeef:0", "deadbeef:1")
		if err := c.Put(ctx, "k1", want, t0); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, ok, err := c.Get(ctx, "k1", t0.Add(-time.Hour))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !ok {
			t.Fatal("want a hit for a freshly stored key")
		}
		if got.Text != want.Text {
			t.Errorf("text = %q, want %q", got.Text, want.Text)
		}
		if len(got.Citations) != 2 || got.Citations[0].ChunkID != "deadbeef:0" || got.Citations[1].Seq != 1 {
			t.Errorf("citations did not round-trip: %+v", got.Citations)
		}
	})

	t.Run("get is a miss for an unknown key", func(t *testing.T) {
		c := factory(t)
		if _, ok, err := c.Get(ctx, "absent", t0.Add(-time.Hour)); err != nil || ok {
			t.Errorf("unknown key: want (false, nil), got (%v, %v)", ok, err)
		}
	})

	t.Run("get is a miss when the entry predates notBefore (expired)", func(t *testing.T) {
		c := factory(t)
		if err := c.Put(ctx, "k1", answer("old"), t0); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if _, ok, err := c.Get(ctx, "k1", t0.Add(time.Hour)); err != nil || ok {
			t.Errorf("expired entry: want (false, nil), got (%v, %v)", ok, err)
		}
		// Exactly at the boundary still counts as fresh (at-or-after).
		if _, ok, err := c.Get(ctx, "k1", t0); err != nil || !ok {
			t.Errorf("entry at the notBefore boundary should be a hit, got (%v, %v)", ok, err)
		}
	})

	t.Run("put replaces a prior entry for the same key", func(t *testing.T) {
		c := factory(t)
		if err := c.Put(ctx, "k1", answer("first"), t0); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := c.Put(ctx, "k1", answer("second"), t0.Add(time.Minute)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, ok, err := c.Get(ctx, "k1", t0.Add(-time.Hour))
		if err != nil || !ok {
			t.Fatalf("Get: ok=%v err=%v", ok, err)
		}
		if got.Text != "second" {
			t.Errorf("text = %q, want the replacement %q", got.Text, "second")
		}
	})

	t.Run("prune deletes entries stored before the cutoff, keeps newer ones", func(t *testing.T) {
		c := factory(t)
		if err := c.Put(ctx, "old", answer("old"), t0); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := c.Put(ctx, "new", answer("new"), t0.Add(2*time.Hour)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := c.Prune(ctx, t0.Add(time.Hour)); err != nil {
			t.Fatalf("Prune: %v", err)
		}
		if _, ok, _ := c.Get(ctx, "old", t0.Add(-time.Hour)); ok {
			t.Error("pruned entry should be gone")
		}
		if _, ok, _ := c.Get(ctx, "new", t0.Add(-time.Hour)); !ok {
			t.Error("newer entry should survive the prune")
		}
	})
}
