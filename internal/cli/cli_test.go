package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/adapters/extract"
	"github.com/jmurray2011/lore/internal/adapters/fs"
	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/cli"
	"github.com/jmurray2011/lore/internal/domain"
)

// The CLI is an integration boundary, so these tests drive it against the
// real memstore reference adapters plus tiny stubs for the network ports.

type stubEmbedder struct {
	space domain.EmbeddingSpace
	vec   []float32
}

func (s stubEmbedder) Space(context.Context) (domain.EmbeddingSpace, error) { return s.space, nil }

func (s stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = s.vec
	}
	return out, nil
}

type stubGenerator struct {
	text string
	rec  *[]domain.Attachment // when set, records the attachments it was given
}

func (s stubGenerator) Synthesize(_ context.Context, _ string, hits []domain.ChunkHit, attachments []domain.Attachment) (app.Answer, error) {
	if s.rec != nil {
		*s.rec = attachments
	}
	cites := make([]domain.ChunkID, len(hits))
	for i, h := range hits {
		cites[i] = h.Chunk.ID
	}
	return app.Answer{Text: s.text, Citations: cites}, nil
}

func newDeps(emb app.Embedder, gen app.Generator) (cli.Deps, *memstore.CollectionRepository, *memstore.DocumentRepository, *memstore.VectorIndex) {
	colls := memstore.NewCollectionRepository()
	docs := memstore.NewDocumentRepository()
	index := memstore.NewVectorIndex()
	q := app.NewQuerier(colls, index, docs, emb)
	chunker, _ := domain.NewChunker(domain.DefaultChunkSize, domain.DefaultChunkOverlap)
	deps := cli.Deps{
		Catalog: app.NewCatalog(colls, emb),
		Ingest:  app.NewIngestor(colls, docs, index, emb, extract.New(), fs.NewSource(), chunker),
		Query:   q,
		Ask:     app.NewAsker(q, gen),
		Remove:  app.NewRemover(colls, docs, index),
	}
	return deps, colls, docs, index
}

// exec runs one command with a fresh root (clean flag state) over shared deps.
func exec(deps cli.Deps, args ...string) (string, int) {
	var out bytes.Buffer
	root := cli.NewRootCommand(deps, "test", &out, io.Discard)
	root.SetArgs(args)
	code := cli.ExitCode(root.Execute())
	return out.String(), code
}

func testSpace() domain.EmbeddingSpace {
	return domain.EmbeddingSpace{Model: "test-embed", Dimensions: 3}
}

func TestCLICollectionLifecycle(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})

	t.Run("init emits JSON", func(t *testing.T) {
		out, code := exec(deps, "init", "docs", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var v collectionViewJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if v.Name != "docs" || v.Model != "test-embed" || v.Dimensions != 3 {
			t.Errorf("view = %+v", v)
		}
	})

	t.Run("ls lists the created collection", func(t *testing.T) {
		out, code := exec(deps, "ls")
		if code != 0 || !strings.Contains(out, "docs") {
			t.Errorf("ls: code %d, out %q", code, out)
		}
	})

	t.Run("status of known collection", func(t *testing.T) {
		out, code := exec(deps, "status", "docs")
		if code != 0 || !strings.Contains(out, "test-embed") {
			t.Errorf("status: code %d, out %q", code, out)
		}
	})

	t.Run("status of unknown collection exits 3", func(t *testing.T) {
		_, code := exec(deps, "status", "ghost")
		if code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})
}

func TestCLIUsageErrors(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	cases := [][]string{
		{"init"},                 // missing name
		{"query", "docs"},        // missing query string
		{"ask", "docs"},          // missing question
		{"query", "a", "b", "c"}, // too many args
	}
	for _, args := range cases {
		if _, code := exec(deps, args...); code != 2 {
			t.Errorf("%v: want exit 2 (usage), got %d", args, code)
		}
	}
}

func TestCLIQuery(t *testing.T) {
	space := testSpace()
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: space, vec: qvec}, stubGenerator{})

	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatalf("init exit %d", code)
	}

	// Populate one chunk + its matching vector directly through the ports.
	ctx := context.Background()
	doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := domain.NewChunk(doc.ID, 0, "the grounded answer")
	if err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, doc, []domain.Chunk{chunk}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: chunk.ID, Vector: qvec}}); err != nil {
		t.Fatal(err)
	}

	t.Run("returns a hit as JSON", func(t *testing.T) {
		out, code := exec(deps, "query", "docs", "anything", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(hits) != 1 || hits[0].ChunkID != string(chunk.ID) || hits[0].Score < 0.99 {
			t.Errorf("hits = %+v", hits)
		}
	})

	t.Run("unknown collection exits 3", func(t *testing.T) {
		_, code := exec(deps, "query", "ghost", "anything")
		if code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})
}

func TestCLISpaceMismatchExits4(t *testing.T) {
	// Collection pinned to one space; embedder reports a different one.
	deps, colls, _, _ := newDeps(stubEmbedder{space: domain.EmbeddingSpace{Model: "other", Dimensions: 9}}, stubGenerator{})
	coll, err := domain.NewCollection("docs", testSpace(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := colls.Create(context.Background(), coll); err != nil {
		t.Fatal(err)
	}

	if _, code := exec(deps, "query", "docs", "anything"); code != 4 {
		t.Errorf("want exit 4 (invariant), got %d", code)
	}
}

func TestCLIAsk(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{text: "the answer"})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	out, code := exec(deps, "ask", "docs", "why?", "--json")
	if code != 0 {
		t.Fatalf("exit %d, out %q", code, out)
	}
	var ans answerViewJSON
	if err := json.Unmarshal([]byte(out), &ans); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if ans.Text != "the answer" {
		t.Errorf("answer = %+v", ans)
	}
}

func TestCLIAskAttach(t *testing.T) {
	t.Run("--attach reads a file into an Attachment passed to the generator", func(t *testing.T) {
		var got []domain.Attachment
		deps, _, _, _ := newDeps(
			stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}},
			stubGenerator{text: "answer", rec: &got},
		)
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		img := filepath.Join(t.TempDir(), "c.png")
		if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4e, 0x47}, 0o600); err != nil {
			t.Fatal(err)
		}

		_, code := exec(deps, "ask", "docs", "what is this?", "--attach", img)
		if code != 0 {
			t.Fatalf("ask --attach exit %d", code)
		}
		if len(got) != 1 || got[0].MediaType != "image/png" || got[0].Name != "c.png" || len(got[0].Data) == 0 {
			t.Errorf("attachment = %+v", got)
		}
	})

	t.Run("unknown file extension is a usage error (exit 2)", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
		exec(deps, "init", "docs")
		mystery := filepath.Join(t.TempDir(), "blob.unknownext")
		if err := os.WriteFile(mystery, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, code := exec(deps, "ask", "docs", "q", "--attach", mystery); code != 2 {
			t.Errorf("want exit 2 for undetectable media type, got %d", code)
		}
	})
}

func TestCLIAddThenQuery(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("hello grounded world"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}

	out, code := exec(deps, "add", "docs", dir, "--json")
	if code != 0 {
		t.Fatalf("add exit %d, out %q", code, out)
	}
	var sum ingestViewJSON
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if sum.Added != 1 || sum.Chunks < 1 {
		t.Errorf("add summary = %+v", sum)
	}

	out, _ = exec(deps, "add", "docs", dir, "--json")
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Added != 0 || sum.Skipped != 1 {
		t.Errorf("re-add must be idempotent: %+v", sum)
	}

	out, code = exec(deps, "query", "docs", "anything", "--json")
	if code != 0 {
		t.Fatalf("query exit %d", code)
	}
	var hits []hitViewJSON
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) < 1 || !strings.Contains(hits[0].Text, "hello grounded world") {
		t.Errorf("hits = %+v", hits)
	}
}

func TestCLIRemove(t *testing.T) {
	t.Run("rm collection removes it", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "d.txt"), []byte("content here"), 0o600); err != nil {
			t.Fatal(err)
		}
		exec(deps, "init", "docs")
		exec(deps, "add", "docs", dir)

		if _, code := exec(deps, "rm", "docs"); code != 0 {
			t.Fatalf("rm exit %d", code)
		}
		if _, code := exec(deps, "status", "docs"); code != 3 {
			t.Errorf("collection should be gone, status exit %d", code)
		}
	})

	t.Run("rm --doc removes one document's content", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
		dir := t.TempDir()
		path := filepath.Join(dir, "d.txt")
		if err := os.WriteFile(path, []byte("content here"), 0o600); err != nil {
			t.Fatal(err)
		}
		exec(deps, "init", "docs")
		exec(deps, "add", "docs", dir)

		abs, _ := filepath.Abs(path)
		uri := "file://" + filepath.ToSlash(abs)
		if _, code := exec(deps, "rm", "docs", "--doc", uri); code != 0 {
			t.Fatalf("rm --doc exit %d", code)
		}
		if _, code := exec(deps, "status", "docs"); code != 0 {
			t.Errorf("collection should remain, status exit %d", code)
		}
		out, _ := exec(deps, "query", "docs", "anything", "--json")
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Errorf("document removed, want no hits, got %+v", hits)
		}
	})

	t.Run("rm unknown collection exits 3", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
		if _, code := exec(deps, "rm", "ghost"); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})
}

// Mirror of the CLI's JSON output shapes, for decoding in tests.
type collectionViewJSON struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  string `json:"created_at"`
}

type hitViewJSON struct {
	ChunkID string  `json:"chunk_id"`
	Score   float64 `json:"score"`
	Text    string  `json:"text"`
}

type answerViewJSON struct {
	Text      string   `json:"text"`
	Citations []string `json:"citations"`
}

type ingestViewJSON struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
	Chunks  int `json:"chunks"`
}
