package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// testSpace is the embedding space the seeded "docs"/"notes" collections share.
var testSpace = domain.EmbeddingSpace{Model: "test-embed", Dimensions: 2}

// otherSpace is a deliberately incompatible space, for cross-collection
// space-mismatch tests.
var otherSpace = domain.EmbeddingSpace{Model: "other-embed", Dimensions: 3}

// fakeEmbedder reports a fixed space and returns the same query vector for any
// text, so seeded chunk vectors rank deterministically by cosine.
type fakeEmbedder struct {
	space domain.EmbeddingSpace
	vec   []float32
}

func (f *fakeEmbedder) Space(context.Context) (domain.EmbeddingSpace, error) { return f.space, nil }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

// fakeGenerator synthesizes a deterministic answer that cites the first (up to
// two) hits inline as [<chunkID>], so renumber/citation handling is exercised.
type fakeGenerator struct{}

func (fakeGenerator) Synthesize(_ context.Context, _ string, hits []domain.ChunkHit, _ []domain.Attachment) (app.Answer, error) {
	if len(hits) == 0 {
		return app.Answer{Text: "I don't know."}, nil
	}
	var b strings.Builder
	b.WriteString("Grounded answer.")
	var cites []domain.Citation
	for i, h := range hits {
		if i >= 2 {
			break
		}
		b.WriteString(" [" + string(h.Chunk.ID) + "]")
		cites = append(cites, domain.Citation{ChunkID: h.Chunk.ID, Source: h.Source, Seq: h.Chunk.Seq, Collection: h.Collection})
	}
	return app.Answer{Text: b.String(), Citations: cites}, nil
}

// fakeReranker scores documents by ascending input index, so the LAST candidate
// ranks first — proving reranking reordered the vector pool.
type fakeReranker struct{ calls int }

func (r *fakeReranker) Rerank(_ context.Context, _ string, documents []string, _ int) ([]app.RankResult, error) {
	r.calls++
	out := make([]app.RankResult, len(documents))
	for i := range documents {
		out[i] = app.RankResult{Index: i, Score: float64(i)}
	}
	return out, nil
}

// fakeTokens counts whitespace-separated words.
type fakeTokens struct{}

func (fakeTokens) Count(text string) int { return len(strings.Fields(text)) }

// countingCollections wraps a CollectionRepository, counting Get calls — the warm
// store spy.
type countingCollections struct {
	app.CollectionRepository
	gets int
}

func (c *countingCollections) Get(ctx context.Context, name string) (*domain.Collection, error) {
	c.gets++
	return c.CollectionRepository.Get(ctx, name)
}

type seedChunk struct {
	text    string
	heading string
	vec     []float32
}

// harness holds a server plus the underlying store handles, so tests can seed and
// inspect.
type harness struct {
	srv     *Server
	colls   app.CollectionRepository
	docs    app.DocumentRepository
	index   app.VectorIndex
	rerank  *fakeReranker
	collSpy *countingCollections
}

// newHarness builds a server over memstore + fakes, seeding "docs" and "notes"
// (same space). withRerank wires a fake rerank provider; scope sets --collections.
func newHarness(t *testing.T, scope []string, withRerank bool) *harness {
	t.Helper()
	spy := &countingCollections{CollectionRepository: memstore.NewCollectionRepository()}
	h := &harness{
		colls:   spy,
		docs:    memstore.NewDocumentRepository(),
		index:   memstore.NewVectorIndex(),
		collSpy: spy,
	}
	seedCollection(t, h, "docs", testSpace, []seedChunk{
		{text: "Authentication uses API keys.", heading: "Auth", vec: []float32{1, 0}},
		{text: "Rotate keys quarterly.", heading: "Auth > Rotation", vec: []float32{0.6, 0.8}},
		{text: "Totally unrelated content.", vec: []float32{0, 1}},
	})
	seedCollection(t, h, "notes", testSpace, []seedChunk{
		{text: "Deployment notes go here.", vec: []float32{0.8, 0.6}},
	})

	emb := &fakeEmbedder{space: testSpace, vec: []float32{1, 0}}
	querier := app.NewQuerier(h.colls, h.index, h.docs, emb)
	catalog := app.NewCatalog(h.colls, h.docs, emb, domain.Registry{})
	deps := Deps{
		Catalog: catalog,
		Query:   querier,
		Ask:     app.NewAsker(querier, fakeGenerator{}),
		Tokens:  fakeTokens{},
		Index:   h.index,
	}
	if withRerank {
		h.rerank = &fakeReranker{}
		deps.Rerank = app.NewReranker(h.rerank)
	}
	srv, err := New(deps, Config{Collections: scope, Version: "test"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.srv = srv
	return h
}

// seedOther adds a collection in an incompatible embedding space.
func (h *harness) seedOther(t *testing.T) {
	t.Helper()
	seedCollection(t, h, "other", otherSpace, []seedChunk{
		{text: "Different space.", vec: []float32{1, 0, 0}},
	})
}

func seedCollection(t *testing.T, h *harness, name string, space domain.EmbeddingSpace, chunks []seedChunk) {
	t.Helper()
	ctx := context.Background()
	spec, err := domain.NewChunkerSpec("fixed", 1, 256, 0, "words", false)
	if err != nil {
		t.Fatalf("chunker spec: %v", err)
	}
	coll, err := domain.NewCollection(name, space, spec, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("new collection: %v", err)
	}
	if err := h.colls.Create(ctx, coll); err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	doc, err := domain.NewDocument(name, "file:///"+name+".md", domain.HashContent([]byte(name)), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("new document: %v", err)
	}
	var cs []domain.Chunk
	var entries []app.VectorEntry
	for i, sc := range chunks {
		ch, err := domain.NewChunk(doc.ID, i, sc.text)
		if err != nil {
			t.Fatalf("new chunk: %v", err)
		}
		ch.HeadingPath = sc.heading
		cs = append(cs, ch)
		entries = append(entries, app.VectorEntry{ChunkID: ch.ID, Vector: sc.vec})
	}
	if err := h.docs.Upsert(ctx, doc, cs); err != nil {
		t.Fatalf("upsert doc %q: %v", name, err)
	}
	if err := h.index.Upsert(ctx, name, entries); err != nil {
		t.Fatalf("upsert vectors %q: %v", name, err)
	}
}

// connect wires an in-process client session to the server via the SDK's
// in-memory transport.
func connect(t *testing.T, srv *Server) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := srv.mcp.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes a tool and fails on a transport/protocol error (a tool error
// is returned in the result, not here).
func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %q (protocol error): %v", name, err)
	}
	return res
}

// decodeOut re-marshals the result's structured content into T.
func decodeOut[T any](t *testing.T, res *mcpsdk.CallToolResult) T {
	t.Helper()
	var out T
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal structured content into %T: %v", out, err)
	}
	return out
}

// textOf returns the first text content block of a result.
func textOf(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
