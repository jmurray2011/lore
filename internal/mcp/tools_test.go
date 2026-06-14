package mcp

import (
	"strings"
	"testing"
)

func TestAsk_GroundedWithCitations(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "how does auth work?"})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", textOf(t, res))
	}
	out := decodeOut[AskOutput](t, res)
	if !out.Grounded {
		t.Error("want grounded answer")
	}
	if len(out.Citations) == 0 {
		t.Fatal("want citations")
	}
	// Ordinals are 1-based and the answer text references them as [n].
	if out.Citations[0].Ordinal != 1 {
		t.Errorf("first citation ordinal = %d, want 1", out.Citations[0].Ordinal)
	}
	if !strings.Contains(out.Answer, "[1]") {
		t.Errorf("answer should reference [1]: %q", out.Answer)
	}
	if strings.Contains(out.Answer, string(out.Citations[0].ChunkID)) {
		t.Error("raw chunk ID leaked into answer text (should be renumbered to [n])")
	}
	// include_sources defaults to true, so cited chunk text is attached.
	if out.Citations[0].Text == "" {
		t.Error("want cited chunk text by default (include_sources)")
	}
	if out.Citations[0].Source == "" {
		t.Error("want citation source URI")
	}
}

func TestAsk_IncludeSourcesFalseOmitsText(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	no := false
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?", IncludeSources: &no})
	out := decodeOut[AskOutput](t, res)
	if len(out.Citations) == 0 {
		t.Fatal("want citations")
	}
	for _, c := range out.Citations {
		if c.Text != "" {
			t.Errorf("citation text should be omitted when include_sources is false, got %q", c.Text)
		}
	}
}

func TestAsk_MultiCollectionMergesAndTags(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs", "notes"}, Question: "auth?", K: 4})
	out := decodeOut[AskOutput](t, res)
	if len(out.Citations) == 0 {
		t.Fatal("want citations")
	}
	// Cross-collection citations carry their origin collection.
	for _, c := range out.Citations {
		if c.Collection == "" {
			t.Errorf("multi-collection citation %q missing collection tag", c.ChunkID)
		}
	}
}

func TestAsk_StrictUngroundedIsToolError(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	// A source glob that matches nothing leaves no grounding.
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?", Strict: true, SourceGlob: "*.pdf"})
	if !res.IsError {
		t.Fatal("want tool error for strict ungrounded ask")
	}
	if !strings.Contains(textOf(t, res), "grounding") {
		t.Errorf("error should mention grounding: %q", textOf(t, res))
	}
}

func TestAsk_NonStrictUngroundedAnswersUngrounded(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?", SourceGlob: "*.pdf"})
	if res.IsError {
		t.Fatalf("non-strict ungrounded should not error: %s", textOf(t, res))
	}
	out := decodeOut[AskOutput](t, res)
	if out.Grounded {
		t.Error("want grounded=false")
	}
}

func TestAsk_RerankUnconfiguredIsToolError(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv) // no rerank provider
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?", Rerank: true})
	if !res.IsError {
		t.Fatal("want tool error when rerank requested without a provider")
	}
	if !strings.Contains(textOf(t, res), "rerank") {
		t.Errorf("error should mention rerank config: %q", textOf(t, res))
	}
}

func TestAsk_SpaceMismatchIsToolErrorNamingOffenders(t *testing.T) {
	h := newHarness(t, nil, false)
	h.seedOther(t)
	cs := connect(t, h.srv)
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs", "other"}, Question: "auth?"})
	if !res.IsError {
		t.Fatal("want tool error for cross-space ask")
	}
	msg := textOf(t, res)
	if !strings.Contains(msg, "docs") || !strings.Contains(msg, "other") {
		t.Errorf("error should name both collections: %q", msg)
	}
}

func TestAsk_UnknownCollectionIsToolErrorServerSurvives(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"nope"}, Question: "auth?"})
	if !res.IsError {
		t.Fatal("want tool error for unknown collection")
	}
	// Server survives: a subsequent valid call succeeds on the same session.
	res2 := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?"})
	if res2.IsError {
		t.Fatalf("session should survive a tool error, got: %s", textOf(t, res2))
	}
}

func TestAsk_BudgetTrimsGrounding(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	// Budget of 1 token keeps only the top chunk → at most one citation.
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?", K: 8, Budget: 1})
	out := decodeOut[AskOutput](t, res)
	if len(out.Citations) != 1 {
		t.Errorf("budget=1 should leave a single grounding chunk, got %d citations", len(out.Citations))
	}
}

func TestAsk_EmptyQuestionIsToolError(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "   "})
	if !res.IsError {
		t.Fatal("want tool error for empty question")
	}
}

func TestQuery_ReturnsHitsWithCollectionTag(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs", "notes"}, Query: "auth", K: 4})
	out := decodeOut[QueryOutput](t, res)
	if len(out.Hits) == 0 {
		t.Fatal("want hits")
	}
	// Best hit first; merged hits carry their origin collection.
	if out.Hits[0].Score < out.Hits[len(out.Hits)-1].Score {
		t.Error("hits should be sorted best-first")
	}
	for _, hit := range out.Hits {
		if hit.Collection == "" {
			t.Errorf("multi-collection hit %q missing collection tag", hit.ChunkID)
		}
		if hit.Text == "" || hit.ChunkID == "" {
			t.Error("hit missing text/chunk_id")
		}
	}
}

func TestQuery_SingleCollectionNoCollectionTag(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth"})
	out := decodeOut[QueryOutput](t, res)
	if len(out.Hits) == 0 {
		t.Fatal("want hits")
	}
	for _, hit := range out.Hits {
		if hit.Collection != "" {
			t.Errorf("single-collection hit should omit collection tag, got %q", hit.Collection)
		}
	}
}

func TestQuery_SourceGlobFilters(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth", SourceGlob: "*.pdf"})
	out := decodeOut[QueryOutput](t, res)
	if len(out.Hits) != 0 {
		t.Errorf("source_glob '*.pdf' should match no markdown docs, got %d hits", len(out.Hits))
	}
}

func TestQuery_RerankReorders(t *testing.T) {
	h := newHarness(t, nil, true)
	cs := connect(t, h.srv)
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth", K: 3, Rerank: true})
	out := decodeOut[QueryOutput](t, res)
	if len(out.Hits) == 0 {
		t.Fatal("want hits")
	}
	if h.rerank.calls == 0 {
		t.Error("rerank provider should have been called")
	}
	for _, hit := range out.Hits {
		if hit.RerankScore == nil {
			t.Error("reranked hit should carry a rerank_score")
		}
	}
}

func TestGetChunks_ReturnsFoundOmitsAbsent(t *testing.T) {
	h := newHarness(t, nil, false)
	cs := connect(t, h.srv)
	// First learn a real chunk ID from a query.
	q := decodeOut[QueryOutput](t, callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth"}))
	realID := q.Hits[0].ChunkID

	res := callTool(t, cs, "get_chunks", GetChunksInput{Collection: "docs", ChunkIDs: []string{realID, "deadbeef:99"}})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", textOf(t, res))
	}
	out := decodeOut[GetChunksOutput](t, res)
	if len(out.Chunks) != 1 {
		t.Fatalf("want exactly the one found chunk, got %d", len(out.Chunks))
	}
	if out.Chunks[0].ChunkID != realID || out.Chunks[0].Text == "" {
		t.Errorf("wrong chunk returned: %+v", out.Chunks[0])
	}
}

func TestGetChunks_UnknownCollectionIsToolError(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "get_chunks", GetChunksInput{Collection: "nope", ChunkIDs: []string{"deadbeef:0"}})
	if !res.IsError {
		t.Fatal("want tool error for unknown collection")
	}
}

func TestListCollections_ReturnsMetadata(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	out := decodeOut[ListCollectionsOutput](t, callTool(t, cs, "list_collections", ListCollectionsInput{}))
	if len(out.Collections) != 2 {
		t.Fatalf("want 2 collections, got %d", len(out.Collections))
	}
	got := out.Collections[0] // sorted: docs before notes
	if got.Name != "docs" {
		t.Errorf("first collection = %q, want docs", got.Name)
	}
	if got.Model != testSpace.Model || got.Dimensions != testSpace.Dimensions {
		t.Errorf("space metadata wrong: %+v", got)
	}
	if got.Documents != 1 {
		t.Errorf("docs document count = %d, want 1", got.Documents)
	}
	if got.Chunker == "" {
		t.Error("want chunker label")
	}
}

func TestCollectionStatus_IncludesChunkCount(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	out := decodeOut[CollectionStatusOutput](t, callTool(t, cs, "collection_status", CollectionStatusInput{Collection: "docs"}))
	if out.Name != "docs" {
		t.Errorf("name = %q, want docs", out.Name)
	}
	if out.Documents != 1 {
		t.Errorf("documents = %d, want 1", out.Documents)
	}
	if out.Chunks != 3 {
		t.Errorf("chunks = %d, want 3 (seeded chunk count)", out.Chunks)
	}
}

func TestCollectionStatus_UnknownIsToolError(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "collection_status", CollectionStatusInput{Collection: "nope"})
	if !res.IsError {
		t.Fatal("want tool error for unknown collection")
	}
}

func TestQuery_HybridRoutesAndReturnsHits(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth", Hybrid: true})
	if res.IsError {
		t.Fatalf("hybrid query should route through the Retriever, got error: %v", res)
	}
	if out := decodeOut[QueryOutput](t, res); len(out.Hits) == 0 {
		t.Error("hybrid query should return hits")
	}
}

func TestQuery_WhereFiltersByMetadata(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	// No seeded doc carries an author, so the predicate matches nothing — proving
	// the where filter reaches the index, not that it is ignored.
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth", Where: []string{"author=nobody"}})
	if res.IsError {
		t.Fatalf("where query errored: %v", res)
	}
	if out := decodeOut[QueryOutput](t, res); len(out.Hits) != 0 {
		t.Errorf("where author=nobody should match nothing, got %d hits", len(out.Hits))
	}
}

func TestQuery_MMRWithRerankIsToolError(t *testing.T) {
	cs := connect(t, newHarness(t, nil, true).srv) // rerank configured
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth", MMR: true, Rerank: true})
	if !res.IsError {
		t.Error("mmr + rerank should be a tool error (both reorder the pool)")
	}
}
