package conformance

import (
	"context"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// RunLexicalIndexSuite verifies the app.LexicalIndex contract. The factory must
// return a fresh, empty index per call.
//
// Assertions are on ranking *order* and membership, never exact scores: an
// in-memory BM25 and SQLite FTS5 compute different absolute scores (and corpus
// statistics), but any reasonable BM25 agrees that a chunk with more occurrences
// of a rare query term outranks one with fewer, and that a chunk containing none
// of the query terms is not a match.
func RunLexicalIndexSuite(t *testing.T, factory func(t *testing.T) app.LexicalIndex) {
	t.Helper()
	ctx := context.Background()

	docs := []app.LexicalDoc{
		{ChunkID: "d1", Text: "alpha alpha alpha beta", Metadata: domain.Metadata{"author": "alice"}},
		{ChunkID: "d2", Text: "alpha beta gamma delta", Metadata: domain.Metadata{"author": "bob"}},
		{ChunkID: "d3", Text: "zeta eta theta iota", Metadata: domain.Metadata{"author": "alice"}},
	}

	contains := func(ids []domain.ChunkID, want domain.ChunkID) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}
	indexOf := func(ids []domain.ChunkID, want domain.ChunkID) int {
		for i, id := range ids {
			if id == want {
				return i
			}
		}
		return -1
	}

	t.Run("ranks chunks containing the query term, more occurrences first", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", docs)

		got, err := idx.Search(ctx, "docs", "alpha", 10, domain.Predicate{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !contains(got, "d1") || !contains(got, "d2") {
			t.Fatalf("both d1 and d2 contain 'alpha', got %v", got)
		}
		if contains(got, "d3") {
			t.Errorf("d3 contains no query term and must not match, got %v", got)
		}
		if indexOf(got, "d1") > indexOf(got, "d2") {
			t.Errorf("d1 (3x alpha) must outrank d2 (1x alpha), got %v", got)
		}
	})

	t.Run("k limits the number of results", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", docs)
		got, err := idx.Search(ctx, "docs", "alpha beta", 1, domain.Predicate{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("k=1 should cap results at 1, got %d: %v", len(got), got)
		}
	})

	t.Run("empty query, k<=0, and unknown collection yield nothing, no error", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", docs)
		for _, tc := range []struct {
			coll, query string
			k           int
		}{
			{"docs", "", 5},
			{"docs", "alpha", 0},
			{"docs", "alpha", -1},
			{"nope", "alpha", 5},
		} {
			got, err := idx.Search(ctx, tc.coll, tc.query, tc.k, domain.Predicate{})
			if err != nil || len(got) != 0 {
				t.Errorf("Search(%q,%q,%d): want empty/nil, got %v/%v", tc.coll, tc.query, tc.k, got, err)
			}
		}
	})

	t.Run("filters by metadata predicate", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", docs)
		where, err := domain.ParseWhere([]string{"author=bob"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := idx.Search(ctx, "docs", "alpha", 10, where)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || got[0] != "d2" {
			t.Errorf("author=bob should leave only d2, got %v", got)
		}
	})

	t.Run("collections are isolated", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", docs[:1])                                                            // d1 in docs
		mustIndex(t, idx, "notes", []app.LexicalDoc{{ChunkID: "n1", Text: "alpha alpha alpha alpha"}}) // n1 in notes

		got, err := idx.Search(ctx, "notes", "alpha", 10, domain.Predicate{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if contains(got, "d1") {
			t.Error("a match from another collection leaked into search")
		}
	})

	t.Run("upsert replaces the same chunk ID", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", []app.LexicalDoc{{ChunkID: "x", Text: "alpha"}})
		// Re-index x with text that no longer contains alpha.
		mustIndex(t, idx, "docs", []app.LexicalDoc{{ChunkID: "x", Text: "omega"}})

		got, err := idx.Search(ctx, "docs", "alpha", 10, domain.Predicate{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if contains(got, "x") {
			t.Error("upsert must replace content, not accumulate it")
		}
	})

	t.Run("delete removes chunks; absent IDs are a no-op", func(t *testing.T) {
		idx := factory(t)
		mustIndex(t, idx, "docs", docs)
		if err := idx.Delete(ctx, "docs", []domain.ChunkID{"d1", "does-not-exist"}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, err := idx.Search(ctx, "docs", "alpha", 10, domain.Predicate{})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if contains(got, "d1") {
			t.Error("deleted chunk still present")
		}
		if !contains(got, "d2") {
			t.Error("delete removed an unrelated chunk")
		}
	})
}

func mustIndex(t *testing.T, idx app.LexicalIndex, collection string, docs []app.LexicalDoc) {
	t.Helper()
	if err := idx.Upsert(context.Background(), collection, docs); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}
