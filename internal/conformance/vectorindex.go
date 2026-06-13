// Package conformance contains executable contracts for the ports defined in
// internal/app. Every adapter must pass the suite for each port it
// implements; behavior questions are settled by adding cases here, never by
// adapter-specific convention (DESIGN.md).
package conformance

import (
	"context"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// RunVectorIndexSuite verifies the app.VectorIndex contract. The factory
// must return a fresh, empty index per call.
func RunVectorIndexSuite(t *testing.T, factory func(t *testing.T) app.VectorIndex) {
	t.Helper()
	ctx := context.Background()

	entries := []app.VectorEntry{
		{ChunkID: "a", Vector: []float32{1, 0, 0}},
		{ChunkID: "b", Vector: []float32{0, 1, 0}},
		{ChunkID: "c", Vector: []float32{0.9, 0.1, 0}},
	}

	t.Run("search returns best matches first", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", entries)

		got, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, 2)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 matches, got %d", len(got))
		}
		if got[0].ChunkID != "a" || got[1].ChunkID != "c" {
			t.Errorf("want [a c], got [%s %s]", got[0].ChunkID, got[1].ChunkID)
		}
		if got[0].Score < got[1].Score {
			t.Errorf("scores must be non-increasing: %f then %f", got[0].Score, got[1].Score)
		}
	})

	t.Run("k larger than index returns all", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", entries)

		got, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, 100)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != len(entries) {
			t.Errorf("want %d matches, got %d", len(entries), len(got))
		}
	})

	t.Run("k <= 0 yields no matches, no error", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", entries)

		for _, k := range []int{0, -1} {
			got, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, k)
			if err != nil || len(got) != 0 {
				t.Errorf("k=%d: want empty, nil; got %v, %v", k, got, err)
			}
		}
	})

	t.Run("unknown collection yields no matches, no error", func(t *testing.T) {
		idx := factory(t)
		got, err := idx.Search(ctx, "nope", []float32{1, 0, 0}, 5)
		if err != nil || len(got) != 0 {
			t.Errorf("want empty, nil; got %v, %v", got, err)
		}
	})

	t.Run("collections are isolated", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", entries[:1])
		mustUpsert(t, idx, "notes", entries[1:2])

		got, err := idx.Search(ctx, "notes", []float32{1, 0, 0}, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		for _, m := range got {
			if m.ChunkID == "a" {
				t.Error("match from another collection leaked into search")
			}
		}
	})

	t.Run("upsert replaces same chunk ID", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", []app.VectorEntry{{ChunkID: "a", Vector: []float32{0, 1, 0}}})
		mustUpsert(t, idx, "docs", []app.VectorEntry{{ChunkID: "a", Vector: []float32{1, 0, 0}}})

		got, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("upsert must replace, not duplicate: got %d entries", len(got))
		}
		if got[0].Score < 0.99 {
			t.Errorf("replaced vector should match query, score %f", got[0].Score)
		}
	})

	t.Run("entries returns every stored vector for the collection", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", entries)
		// Isolation: a distinct chunk in another collection must not leak. Chunk
		// IDs are globally unique (the doc hash includes the collection), so this
		// uses a fresh ID rather than reusing one of "docs"'.
		mustUpsert(t, idx, "notes", []app.VectorEntry{{ChunkID: "z", Vector: []float32{0, 0, 1}}})

		got, err := idx.Entries(ctx, "docs")
		if err != nil {
			t.Fatalf("Entries: %v", err)
		}
		if len(got) != len(entries) {
			t.Fatalf("want %d entries, got %d", len(entries), len(got))
		}
		byID := make(map[domain.ChunkID][]float32, len(got))
		for _, e := range got {
			byID[e.ChunkID] = e.Vector
		}
		for _, want := range entries {
			vec, ok := byID[want.ChunkID]
			if !ok {
				t.Errorf("entry %s missing", want.ChunkID)
				continue
			}
			if len(vec) != len(want.Vector) {
				t.Errorf("entry %s: vector len %d, want %d", want.ChunkID, len(vec), len(want.Vector))
				continue
			}
			for i := range vec {
				if vec[i] != want.Vector[i] {
					t.Errorf("entry %s: vector %v, want %v", want.ChunkID, vec, want.Vector)
					break
				}
			}
		}
	})

	t.Run("entries on an unknown collection is empty, no error", func(t *testing.T) {
		idx := factory(t)
		got, err := idx.Entries(ctx, "nope")
		if err != nil || len(got) != 0 {
			t.Errorf("want empty, nil; got %v, %v", got, err)
		}
	})

	t.Run("entries returns copies; caller mutation does not corrupt the index", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", []app.VectorEntry{{ChunkID: "a", Vector: []float32{1, 0, 0}}})

		got, err := idx.Entries(ctx, "docs")
		if err != nil || len(got) != 1 {
			t.Fatalf("Entries: %v, %v", got, err)
		}
		got[0].Vector[0] = -1 // mutate the returned vector

		again, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, 1)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(again) != 1 || again[0].Score < 0.99 {
			t.Errorf("index must return a copy from Entries; got %+v", again)
		}
	})

	t.Run("delete removes vectors; absent IDs are a no-op", func(t *testing.T) {
		idx := factory(t)
		mustUpsert(t, idx, "docs", entries)

		if err := idx.Delete(ctx, "docs", []domain.ChunkID{"a", "does-not-exist"}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 remaining, got %d", len(got))
		}
		for _, m := range got {
			if m.ChunkID == "a" {
				t.Error("deleted chunk still present")
			}
		}
	})

	t.Run("caller mutations do not corrupt the index", func(t *testing.T) {
		idx := factory(t)
		vec := []float32{1, 0, 0}
		mustUpsert(t, idx, "docs", []app.VectorEntry{{ChunkID: "a", Vector: vec}})
		vec[0] = -1 // mutate after upsert

		got, err := idx.Search(ctx, "docs", []float32{1, 0, 0}, 1)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || got[0].Score < 0.99 {
			t.Errorf("index must store a copy; got %+v", got)
		}
	})
}

func mustUpsert(t *testing.T, idx app.VectorIndex, collection string, entries []app.VectorEntry) {
	t.Helper()
	if err := idx.Upsert(context.Background(), collection, entries); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}
