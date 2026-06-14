package memstore

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// BM25 parameters (Lucene defaults): k1 controls term-frequency saturation, b the
// document-length normalization.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// lexEntry is one indexed chunk: its term frequencies, token length, and the
// document metadata a --where filter is evaluated against.
type lexEntry struct {
	tf   map[string]int
	len  int
	meta domain.Metadata
}

// LexicalIndex is a thread-safe in-memory BM25 keyword index — the reference
// semantics for the lexical half of hybrid retrieval. Corpus statistics (idf,
// average document length) are recomputed per search; for the small corpora
// memstore targets this is cheap and keeps the store free of incremental bookkeeping.
type LexicalIndex struct {
	mu          sync.RWMutex
	collections map[string]map[domain.ChunkID]lexEntry
}

// compile-time port check
var _ app.LexicalIndex = (*LexicalIndex)(nil)

// NewLexicalIndex returns an empty index.
func NewLexicalIndex() *LexicalIndex {
	return &LexicalIndex{collections: make(map[string]map[domain.ChunkID]lexEntry)}
}

// Upsert tokenizes and stores each document, replacing entries with the same ChunkID.
func (x *LexicalIndex) Upsert(_ context.Context, collection string, docs []app.LexicalDoc) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	col := x.collections[collection]
	if col == nil {
		col = make(map[domain.ChunkID]lexEntry, len(docs))
		x.collections[collection] = col
	}
	for _, d := range docs {
		toks := tokenize(d.Text)
		tf := make(map[string]int, len(toks))
		for _, tok := range toks {
			tf[tok]++
		}
		col[d.ChunkID] = lexEntry{tf: tf, len: len(toks), meta: d.Metadata.Clone()}
	}
	return nil
}

// Search ranks the metadata-matching chunks that contain at least one query term
// by BM25, best first, returning up to k chunk IDs.
func (x *LexicalIndex) Search(_ context.Context, collection, query string, k int, filter domain.Predicate) ([]domain.ChunkID, error) {
	if k <= 0 {
		return nil, nil
	}
	terms := uniqueTerms(tokenize(query))
	if len(terms) == 0 {
		return nil, nil
	}

	x.mu.RLock()
	defer x.mu.RUnlock()

	col := x.collections[collection]
	if len(col) == 0 {
		return nil, nil
	}

	// Corpus statistics over the whole collection (standard BM25 idf/avgdl).
	n := float64(len(col))
	totalLen := 0
	df := make(map[string]int)
	for _, e := range col {
		totalLen += e.len
		for term := range e.tf {
			df[term]++
		}
	}
	avgdl := float64(totalLen) / n

	type scored struct {
		id    domain.ChunkID
		score float64
	}
	var results []scored
	for id, e := range col {
		if !filter.Match(e.meta) {
			continue
		}
		var score float64
		matched := false
		for _, t := range terms {
			tf := e.tf[t]
			if tf == 0 {
				continue
			}
			matched = true
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			denom := float64(tf) + bm25K1*(1-bm25B+bm25B*float64(e.len)/avgdl)
			score += idf * (float64(tf) * (bm25K1 + 1)) / denom
		}
		if matched {
			results = append(results, scored{id, score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].id < results[j].id // deterministic ties
	})
	if len(results) > k {
		results = results[:k]
	}
	out := make([]domain.ChunkID, len(results))
	for i, r := range results {
		out[i] = r.id
	}
	return out, nil
}

// Delete removes the given chunk IDs; absent IDs are a no-op.
func (x *LexicalIndex) Delete(_ context.Context, collection string, ids []domain.ChunkID) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	col := x.collections[collection]
	for _, id := range ids {
		delete(col, id)
	}
	return nil
}

// tokenize lowercases and splits text into alphanumeric terms — close enough to
// SQLite FTS5's default unicode61 tokenizer for the two adapters to agree on
// ranking order, which is all the lexical conformance suite asserts.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// uniqueTerms removes duplicate query terms so a repeated word is not weighted twice.
func uniqueTerms(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
