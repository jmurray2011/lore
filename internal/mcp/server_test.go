package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegistration_AllToolsPresentReadOnly(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res, err := cs.ListTools(context.Background(), &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	want := map[string]bool{"ask": false, "query": false, "get_chunks": false, "list_collections": false, "collection_status": false}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool registered: %q", tool.Name)
			continue
		}
		want[tool.Name] = true
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q should be annotated read-only", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestRegistration_AskInputSchemaInferred(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	res, err := cs.ListTools(context.Background(), &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var ask *mcpsdk.Tool
	for _, tool := range res.Tools {
		if tool.Name == "ask" {
			ask = tool
		}
	}
	if ask == nil || ask.InputSchema == nil {
		t.Fatal("ask tool has no input schema")
	}
	schema, err := json.Marshal(ask.InputSchema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	s := string(schema)
	for _, field := range []string{"collections", "question", "rerank", "budget", "include_sources"} {
		if !strings.Contains(s, field) {
			t.Errorf("ask input schema missing %q: %s", field, s)
		}
	}
	// collections + question are required (no omitempty, non-pointer).
	if !strings.Contains(s, "required") {
		t.Errorf("ask input schema should mark required fields: %s", s)
	}
}

func TestScope_RestrictsListingAndCalls(t *testing.T) {
	cs := connect(t, newHarness(t, []string{"docs"}, false).srv)

	// list_collections shows only the in-scope collection.
	out := decodeOut[ListCollectionsOutput](t, callTool(t, cs, "list_collections", ListCollectionsInput{}))
	if len(out.Collections) != 1 || out.Collections[0].Name != "docs" {
		t.Fatalf("scope should expose only docs, got %+v", out.Collections)
	}

	// An out-of-scope collection is a tool error, even though it exists.
	res := callTool(t, cs, "query", QueryInput{Collections: []string{"notes"}, Query: "x"})
	if !res.IsError {
		t.Error("out-of-scope collection should be a tool error")
	}
	res = callTool(t, cs, "collection_status", CollectionStatusInput{Collection: "notes"})
	if !res.IsError {
		t.Error("out-of-scope collection_status should be a tool error")
	}
}

func TestScope_GlobMatches(t *testing.T) {
	cs := connect(t, newHarness(t, []string{"doc*"}, false).srv)
	out := decodeOut[ListCollectionsOutput](t, callTool(t, cs, "list_collections", ListCollectionsInput{}))
	if len(out.Collections) != 1 || out.Collections[0].Name != "docs" {
		t.Fatalf("glob 'doc*' should match only docs, got %+v", out.Collections)
	}
}

func TestNew_InvalidScopePatternErrors(t *testing.T) {
	h := newHarness(t, nil, false)
	_, err := New(Deps{Catalog: h.srv.deps.Catalog}, Config{Collections: []string{"["}}, nil)
	if err == nil {
		t.Fatal("want error for malformed glob pattern")
	}
}

func TestWarm_StoreReusedAcrossCalls(t *testing.T) {
	h := newHarness(t, nil, false)
	cs := connect(t, h.srv)
	const n = 5
	for i := 0; i < n; i++ {
		res := callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?"})
		if res.IsError {
			t.Fatalf("call %d errored: %s", i, textOf(t, res))
		}
	}
	// Each ask resolved the collection through the one warm repository instance;
	// nothing re-opened storage per call (the spy is a single shared handle).
	if h.collSpy.gets < n {
		t.Errorf("warm store should serve every call: got %d Get calls across %d asks", h.collSpy.gets, n)
	}
}

func TestResilience_SequentialCallsAcrossTools(t *testing.T) {
	cs := connect(t, newHarness(t, nil, false).srv)
	if callTool(t, cs, "list_collections", ListCollectionsInput{}).IsError {
		t.Fatal("list_collections errored")
	}
	if callTool(t, cs, "query", QueryInput{Collections: []string{"docs"}, Query: "auth"}).IsError {
		t.Fatal("query errored")
	}
	// A deliberate tool error in the middle...
	if !callTool(t, cs, "ask", AskInput{Collections: []string{"nope"}, Question: "x"}).IsError {
		t.Fatal("expected tool error")
	}
	// ...does not end the session.
	if callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?"}).IsError {
		t.Fatal("session should survive a prior tool error")
	}
}

// TestStdoutClean asserts that a tool call leaks nothing to the process stdout.
// The in-memory transport never writes to os.Stdout, so any captured bytes would
// be a stray print/log — the exact failure that corrupts the stdio protocol.
func TestStdoutClean(t *testing.T) {
	h := newHarness(t, nil, false)

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	cs := connect(t, h.srv)
	_ = callTool(t, cs, "ask", AskInput{Collections: []string{"docs"}, Question: "auth?"})
	_ = callTool(t, cs, "list_collections", ListCollectionsInput{})
	_ = w.Close()
	os.Stdout = old

	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	if len(leaked) != 0 {
		t.Errorf("tool calls leaked to stdout (corrupts the stdio protocol): %q", string(leaked))
	}
}
