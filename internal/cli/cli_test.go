package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	cites := make([]domain.Citation, len(hits))
	for i, h := range hits {
		cites[i] = domain.Citation{ChunkID: h.Chunk.ID, Source: h.Source, Seq: h.Chunk.Seq}
	}
	return app.Answer{Text: s.text, Citations: cites}, nil
}

func newDeps(emb app.Embedder, gen app.Generator) (cli.Deps, *memstore.CollectionRepository, *memstore.DocumentRepository, *memstore.VectorIndex) {
	colls := memstore.NewCollectionRepository()
	docs := memstore.NewDocumentRepository()
	index := memstore.NewVectorIndex()
	q := app.NewQuerier(colls, index, docs, emb)
	chunker, _ := domain.NewChunker(domain.DefaultChunkSize, domain.DefaultChunkOverlap)
	source := fs.NewSource()
	catalog := app.NewCatalog(colls, docs, emb)
	ingestor := app.NewIngestor(colls, docs, index, emb, extract.New(), source, chunker)
	remover := app.NewRemover(colls, docs, index)
	deps := cli.Deps{
		Catalog: catalog,
		Ingest:  ingestor,
		Sync:    app.NewSyncer(catalog, ingestor, remover, source),
		Query:   q,
		Ask:     app.NewAsker(q, gen),
		Remove:  remover,
	}
	return deps, colls, docs, index
}

// exec runs one command with a fresh root (clean flag state) over shared deps.
func exec(deps cli.Deps, args ...string) (string, int) {
	var out bytes.Buffer
	root := cli.NewRootCommand(depsBuilder(deps), "test", &out, io.Discard)
	root.SetArgs(args)
	code := cli.ExitCode(root.Execute())
	return out.String(), code
}

func testSpace() domain.EmbeddingSpace {
	return domain.EmbeddingSpace{Model: "test-embed", Dimensions: 3}
}

func TestRootBuildsDepsFromGlobalFlags(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	var got cli.GlobalOptions
	build := func(_ context.Context, opts cli.GlobalOptions) (cli.Deps, error) {
		got = opts
		return deps, nil
	}
	var out bytes.Buffer
	root := cli.NewRootCommand(build, "test", &out, io.Discard)
	root.SetArgs([]string{"--config", "/tmp/x.toml", "--log-level", "debug", "--log-format", "json", "-v", "ls"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.ConfigPath != "/tmp/x.toml" {
		t.Errorf("ConfigPath = %q, want /tmp/x.toml", got.ConfigPath)
	}
	if got.LogLevel != "debug" || got.LogFormat != "json" {
		t.Errorf("log opts = %+v", got)
	}
	if !got.Verbose {
		t.Error("Verbose should be true with -v")
	}
}

func TestRootBuildErrorPropagates(t *testing.T) {
	build := func(context.Context, cli.GlobalOptions) (cli.Deps, error) {
		return cli.Deps{}, fmt.Errorf("%w: bad config", domain.ErrInvalidArgument)
	}
	var out bytes.Buffer
	root := cli.NewRootCommand(build, "test", &out, io.Discard)
	root.SetArgs([]string{"ls"})
	if code := cli.ExitCode(root.Execute()); code != 2 {
		t.Errorf("build error should surface as exit 2, got %d", code)
	}
}

// depsBuilder wraps already-built deps in a Builder for tests that don't care
// about config resolution.
func depsBuilder(deps cli.Deps) cli.Builder {
	return func(context.Context, cli.GlobalOptions) (cli.Deps, error) { return deps, nil }
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

func TestCLIDocs(t *testing.T) {
	deps, _, docs, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}

	ctx := context.Background()
	for _, uri := range []string{"file:///b.md", "file:///a.md"} { // unsorted on purpose
		doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := docs.Upsert(ctx, doc, nil); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("lists a collection's documents sorted by source, as JSON", func(t *testing.T) {
		out, code := exec(deps, "docs", "docs", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var list []docViewJSON
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(list) != 2 {
			t.Fatalf("want 2 docs, got %d", len(list))
		}
		if list[0].Source != "file:///a.md" || list[1].Source != "file:///b.md" {
			t.Errorf("not sorted by source: %+v", list)
		}
		if list[0].Hash == "" || list[0].IngestedAt == "" {
			t.Errorf("missing hash/ingested_at: %+v", list[0])
		}
	})

	t.Run("unknown collection exits 3", func(t *testing.T) {
		if _, code := exec(deps, "docs", "ghost"); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})
}

func TestCLISync(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
	if _, code := exec(deps, "init", "notes"); code != 0 {
		t.Fatal("init failed")
	}

	dir := t.TempDir()
	b := filepath.Join(dir, "b.txt")
	for name, body := range map[string]string{"a.txt": "alpha content here", "b.txt": "beta content here"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, code := exec(deps, "add", "notes", dir); code != 0 {
		t.Fatalf("add exit %d", code)
	}
	if n := docCount(t, deps, "notes"); n != 2 {
		t.Fatalf("after add: want 2 docs, got %d", n)
	}

	// Delete one source file, then sync with no path: the remembered root is
	// replayed, and --prune removes the document whose file is gone.
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	out, code := exec(deps, "sync", "notes", "--prune", "--json")
	if code != 0 {
		t.Fatalf("sync exit %d, out %q", code, out)
	}
	var sv syncViewJSON
	if err := json.Unmarshal([]byte(out), &sv); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if sv.Pruned != 1 {
		t.Errorf("want Pruned 1, got %+v", sv)
	}
	if n := docCount(t, deps, "notes"); n != 1 {
		t.Errorf("after prune: want 1 doc, got %d", n)
	}

	t.Run("no path and no remembered source exits 2", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
		if _, code := exec(deps, "init", "fresh"); code != 0 {
			t.Fatal("init failed")
		}
		if _, code := exec(deps, "sync", "fresh"); code != 2 {
			t.Errorf("want exit 2 (usage), got %d", code)
		}
	})
}

// docCount returns how many documents `docs <collection> --json` reports.
func docCount(t *testing.T, deps cli.Deps, collection string) int {
	t.Helper()
	out, code := exec(deps, "docs", collection, "--json")
	if code != 0 {
		t.Fatalf("docs exit %d, out %q", code, out)
	}
	var list []docViewJSON
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("bad docs JSON %q: %v", out, err)
	}
	return len(list)
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
		if hits[0].Source != "file:///a.md" || hits[0].Seq != 0 {
			t.Errorf("hit provenance = %q#%d, want file:///a.md#0", hits[0].Source, hits[0].Seq)
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
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer"})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}

	// Seed one chunk + matching vector so the answer carries a citation.
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
	if len(ans.Citations) != 1 || ans.Citations[0].Source != "file:///a.md" || ans.Citations[0].Seq != 0 {
		t.Errorf("citation provenance = %+v, want one file:///a.md#0", ans.Citations)
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

func TestCLIAddCountsUnsupportedSeparately(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello grounded world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
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
	if sum.Added != 1 || sum.Unsupported != 1 || sum.Skipped != 0 {
		t.Errorf("want Added 1 Unsupported 1 Skipped 0, got %+v", sum)
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

type docViewJSON struct {
	Source     string `json:"source"`
	Hash       string `json:"hash"`
	IngestedAt string `json:"ingested_at"`
}

type syncViewJSON struct {
	Added       int `json:"added"`
	Skipped     int `json:"skipped"`
	Unsupported int `json:"unsupported"`
	Chunks      int `json:"chunks"`
	Pruned      int `json:"pruned"`
}

type hitViewJSON struct {
	ChunkID string  `json:"chunk_id"`
	Source  string  `json:"source"`
	Seq     int     `json:"seq"`
	Score   float64 `json:"score"`
	Text    string  `json:"text"`
}

type answerViewJSON struct {
	Text      string `json:"text"`
	Citations []struct {
		ChunkID string `json:"chunk_id"`
		Source  string `json:"source"`
		Seq     int    `json:"seq"`
	} `json:"citations"`
}

type ingestViewJSON struct {
	Added       int `json:"added"`
	Skipped     int `json:"skipped"`
	Unsupported int `json:"unsupported"`
	Chunks      int `json:"chunks"`
}
