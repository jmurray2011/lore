package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/jmurray2011/lore/internal/adapters/agecrypt"
	"github.com/jmurray2011/lore/internal/adapters/extract"
	"github.com/jmurray2011/lore/internal/adapters/fs"
	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/artifact"
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
	text      string
	rec       *[]domain.Attachment // when set, records the attachments it was given
	citeFirst int                  // when >0, cite only the first N hits (else all)
}

func (s stubGenerator) Synthesize(_ context.Context, _ string, hits []domain.ChunkHit, attachments []domain.Attachment) (app.Answer, error) {
	if s.rec != nil {
		*s.rec = attachments
	}
	n := len(hits)
	if s.citeFirst > 0 && s.citeFirst < n {
		n = s.citeFirst
	}
	cites := make([]domain.Citation, n)
	for i := 0; i < n; i++ {
		cites[i] = domain.Citation{ChunkID: hits[i].Chunk.ID, Source: hits[i].Source, Seq: hits[i].Chunk.Seq, Collection: hits[i].Collection}
	}
	return app.Answer{Text: s.text, Citations: cites}, nil
}

// stubStreamingGen implements app.StreamingGenerator, emitting its answer
// word-by-word so a CLI test can observe streamed tokens reach stdout.
type stubStreamingGen struct{ text string }

func (s stubStreamingGen) Synthesize(_ context.Context, _ string, _ []domain.ChunkHit, _ []domain.Attachment) (app.Answer, error) {
	return app.Answer{Text: s.text}, nil
}

func (s stubStreamingGen) SynthesizeStream(_ context.Context, _ string, _ []domain.ChunkHit, _ []domain.Attachment, onDelta func(string)) (app.Answer, error) {
	for i, w := range strings.Fields(s.text) {
		if i > 0 {
			onDelta(" ")
		}
		onDelta(w)
	}
	return app.Answer{Text: s.text}, nil
}

func TestCLIAskStream(t *testing.T) {
	qvec := []float32{1, 0, 0}
	seed := func(t *testing.T, gen app.Generator) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, gen)
		if _, code := exec(deps, "init", "kb"); code != 0 {
			t.Fatal("init failed")
		}
		seedChunk(t, docs, index, "kb", "file:///a.md", 0, "the grounded passage", qvec)
		return deps
	}

	t.Run("--stream emits tokens then a Sources block", func(t *testing.T) {
		deps := seed(t, stubStreamingGen{text: "the grounded answer [1] indeed"})
		out, code := exec(deps, "ask", "kb", "why?", "-k", "1", "--stream")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		if !strings.Contains(out, "the grounded answer [1] indeed") {
			t.Errorf("streamed prose missing from stdout: %q", out)
		}
		if !strings.Contains(out, "## Sources") || !strings.Contains(out, "[1] a.md · chunk 0") {
			t.Errorf("Sources block (keyed to the model's [1]) missing: %q", out)
		}
	})

	t.Run("--stream and --no-stream together is a usage error", func(t *testing.T) {
		deps := seed(t, stubStreamingGen{text: "x [1]"})
		if _, code := exec(deps, "ask", "kb", "q", "--stream", "--no-stream"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("--json suppresses streaming and returns a buffered answer object", func(t *testing.T) {
		deps := seed(t, stubStreamingGen{text: "buffered [1]"})
		out, code := exec(deps, "ask", "kb", "q", "-k", "1", "--stream", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("--json should still emit a JSON answer, got %q: %v", out, err)
		}
		if ans.Text != "buffered [1]" {
			t.Errorf("answer text = %q", ans.Text)
		}
	})

	t.Run("a non-streaming generator falls back to one whole-text emission", func(t *testing.T) {
		deps := seed(t, stubGenerator{text: "fallback answer [1]"})
		out, code := exec(deps, "ask", "kb", "q", "-k", "1", "--stream")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		if !strings.Contains(out, "fallback answer [1]") || !strings.Contains(out, "## Sources") {
			t.Errorf("fallback streamed output wrong: %q", out)
		}
	})
}

// stubRerankProvider reverses the input order (last document = most relevant,
// descending scores), so reranking is visibly distinct from vector order.
type stubRerankProvider struct {
	err    error
	called bool
}

func (s *stubRerankProvider) Rerank(_ context.Context, _ string, docs []string, _ int) ([]app.RankResult, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	out := make([]app.RankResult, len(docs))
	for i := range docs {
		out[i] = app.RankResult{Index: len(docs) - 1 - i, Score: float64(len(docs) - i)}
	}
	return out, nil
}

// withReranker attaches a Reranker use case (over prov) to deps.
func withReranker(deps cli.Deps, prov app.RerankProvider) cli.Deps {
	deps.Rerank = app.NewReranker(prov)
	return deps
}

func newDeps(emb app.Embedder, gen app.Generator) (cli.Deps, *memstore.CollectionRepository, *memstore.DocumentRepository, *memstore.VectorIndex) {
	colls := memstore.NewCollectionRepository()
	docs := memstore.NewDocumentRepository()
	index := memstore.NewVectorIndex()
	lexical := memstore.NewLexicalIndex()
	q := app.NewQuerier(colls, index, docs, emb, lexical)
	fixed, _ := domain.NewFixedChunker(domain.DefaultChunkSize, domain.DefaultChunkOverlap)
	chunkers, _ := domain.NewRegistry(testChunkerSpec(), fixed, nil)
	source := fs.NewSource()
	catalog := app.NewCatalog(colls, docs, emb, chunkers)
	ingestor := app.NewIngestor(colls, docs, index, emb, extract.New(), source, chunkers, lexical)
	remover := app.NewRemover(colls, docs, index, lexical)
	asker := app.NewAsker(q, gen)
	checker := app.NewChecker(stubVerifier{}, catalog)
	deps := cli.Deps{
		Catalog: catalog,
		Ingest:  ingestor,
		Sync:    app.NewSyncer(catalog, ingestor, remover, source),
		Query:   q,
		Ask:     asker,
		Remove:  remover,
		Tokens:  wordTokenCounter{},
		Export:  app.NewExporter(colls, docs, index),
		Import:  app.NewImporter(colls, docs, index, remover, lexical),
		Verify:  checker,
		Eval:    app.NewEvaluator(q, asker, checker),
		Index:   index,
	}
	return deps, colls, docs, index
}

// stubVerifier judges a claim supported unless its text is in unsupported (keyed
// on the segmented claim text, citation markers stripped).
type stubVerifier struct{ unsupported map[string]bool }

func (v stubVerifier) Verify(_ context.Context, claim, _ string) (app.Verdict, error) {
	if v.unsupported[claim] {
		return app.Verdict{Supported: false, Rationale: "not entailed"}, nil
	}
	return app.Verdict{Supported: true, Rationale: "entailed"}, nil
}

// wordTokenCounter is a deterministic stand-in for the real tiktoken counter in
// CLI tests: one token per whitespace word, so --budget arithmetic is easy to
// assert without depending on a BPE vocabulary.
type wordTokenCounter struct{}

func (wordTokenCounter) Count(s string) int { return len(strings.Fields(s)) }

// exec runs one command with a fresh root (clean flag state) over shared deps.
func exec(deps cli.Deps, args ...string) (string, int) {
	var out bytes.Buffer
	root := cli.NewRootCommand(depsBuilder(deps), "test", &out, io.Discard)
	root.SetArgs(args)
	code := cli.ExitCode(root.Execute())
	return out.String(), code
}

// execErr is exec but also returns whatever the command wrote to stderr.
func execErr(deps cli.Deps, args ...string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	root := cli.NewRootCommand(depsBuilder(deps), "test", &out, &errb)
	root.SetArgs(args)
	code = cli.ExitCode(root.Execute())
	return out.String(), errb.String(), code
}

// execStdin is exec with the given string fed to the command on stdin.
func execStdin(deps cli.Deps, stdin string, args ...string) (string, int) {
	var out bytes.Buffer
	root := cli.NewRootCommand(depsBuilder(deps), "test", &out, io.Discard)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	code := cli.ExitCode(root.Execute())
	return out.String(), code
}

func testSpace() domain.EmbeddingSpace {
	return domain.EmbeddingSpace{Model: "test-embed", Dimensions: 3}
}

// testChunkerSpec matches the fixed chunker the test deps wire, so collections
// init'd through the CLI accept ingestion.
func testChunkerSpec() domain.ChunkerSpec {
	return domain.ChunkerSpec{
		Strategy: "fixed", Version: domain.FixedChunkerVersion,
		Size: domain.DefaultChunkSize, Overlap: domain.DefaultChunkOverlap, Tokenizer: "words",
	}
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

func TestCLICat(t *testing.T) {
	deps, _, docs, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("a")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c0, _ := domain.NewChunk(docID, 0, "the first chunk")
	c1, _ := domain.NewChunk(docID, 1, "the second chunk")
	if err := docs.Upsert(ctx, doc, []domain.Chunk{c0, c1}); err != nil {
		t.Fatal(err)
	}

	t.Run("prints a document's chunks in seq order as JSON", func(t *testing.T) {
		out, code := exec(deps, "cat", "docs", "--doc", "file:///a.md", "--json")
		if code != 0 {
			t.Fatalf("cat exit %d, out %q", code, out)
		}
		var got []chunkViewJSON
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(got) != 2 || got[0].Seq != 0 || got[0].Text != "the first chunk" || got[1].Text != "the second chunk" {
			t.Errorf("chunks = %+v", got)
		}
	})

	t.Run("unknown document exits 3", func(t *testing.T) {
		if _, code := exec(deps, "cat", "docs", "--doc", "file:///ghost.md"); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})

	t.Run("missing --doc is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "cat", "docs"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("--chunk prints chunks by ID in input order, as JSON", func(t *testing.T) {
		out, code := exec(deps, "cat", "docs", "--chunk", string(c1.ID), "--chunk", string(c0.ID), "--json")
		if code != 0 {
			t.Fatalf("cat --chunk exit %d, out %q", code, out)
		}
		var got []chunkViewJSON
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(got) != 2 || got[0].ChunkID != string(c1.ID) || got[1].ChunkID != string(c0.ID) {
			t.Errorf("chunks (input order) = %+v", got)
		}
	})

	t.Run("--chunk human output reuses the per-chunk serializer", func(t *testing.T) {
		out, code := exec(deps, "cat", "docs", "--chunk", string(c0.ID))
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(out, "**chunk 0**") || !strings.Contains(out, "the first chunk") {
			t.Errorf("human output = %q", out)
		}
	})

	t.Run("--chunk and --doc together is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "cat", "docs", "--doc", "file:///a.md", "--chunk", string(c0.ID)); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("malformed chunk ID is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "cat", "docs", "--chunk", "not-a-valid-id"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("partial miss prints found on stdout, warns on stderr, exits 3", func(t *testing.T) {
		ghost := string(domain.DeriveChunkID(docID, 99))
		out, errOut, code := execErr(deps, "cat", "docs", "--chunk", string(c0.ID), "--chunk", ghost, "--json")
		if code != 3 {
			t.Fatalf("want exit 3, got %d", code)
		}
		var got []chunkViewJSON
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(got) != 1 || got[0].ChunkID != string(c0.ID) {
			t.Errorf("found chunk should still print: %+v", got)
		}
		if !strings.Contains(errOut, ghost) || !strings.Contains(errOut, "not found") {
			t.Errorf("want a per-ID warning on stderr, got %q", errOut)
		}
	})

	t.Run("all missing exits 3", func(t *testing.T) {
		ghost := string(domain.DeriveChunkID(docID, 99))
		if _, code := exec(deps, "cat", "docs", "--chunk", ghost); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})

	t.Run("unknown collection exits 3", func(t *testing.T) {
		if _, code := exec(deps, "cat", "ghostcoll", "--chunk", string(c0.ID)); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})
}

func TestCLIStatusDocCount(t *testing.T) {
	deps, _, docs, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	for _, uri := range []string{"file:///a.md", "file:///b.md"} {
		doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := docs.Upsert(ctx, doc, nil); err != nil {
			t.Fatal(err)
		}
	}

	out, code := exec(deps, "status", "docs", "--json")
	if code != 0 {
		t.Fatalf("status exit %d, out %q", code, out)
	}
	var v statusViewJSON
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if v.Documents != 2 {
		t.Errorf("documents = %d, want 2", v.Documents)
	}
	if v.Model != "test-embed" {
		t.Errorf("status still carries collection details: %+v", v)
	}
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

func TestCLISyncDryRun(t *testing.T) {
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
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}

	// Dry run reports b.txt as prunable but removes nothing.
	out, code := exec(deps, "sync", "notes", "--prune", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run exit %d, out %q", code, out)
	}
	var sv syncViewJSON
	if err := json.Unmarshal([]byte(out), &sv); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if sv.Pruned != 1 || len(sv.PrunedURIs) != 1 || !strings.Contains(sv.PrunedURIs[0], "b.txt") {
		t.Errorf("want one prunable b.txt, got %+v", sv)
	}
	if !sv.DryRun {
		t.Error("dry_run flag should be set")
	}
	if n := docCount(t, deps, "notes"); n != 2 {
		t.Errorf("dry run must not remove anything; doc count = %d, want 2", n)
	}

	t.Run("--dry-run without --prune is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "sync", "notes", "--dry-run"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
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

func TestCLIQuerySourceFilter(t *testing.T) {
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	for _, uri := range []string{"file:///a.md", "file:///b.pdf"} {
		did := domain.DeriveDocumentID("docs", uri)
		doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ch, err := domain.NewChunk(did, 0, "content of "+uri)
		if err != nil {
			t.Fatal(err)
		}
		if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
			t.Fatal(err)
		}
		if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: qvec}}); err != nil {
			t.Fatal(err)
		}
	}

	out, code := exec(deps, "query", "docs", "anything", "--source", "*.pdf", "--json")
	if code != 0 {
		t.Fatalf("query exit %d, out %q", code, out)
	}
	var hits []hitViewJSON
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Source, "b.pdf") {
		t.Errorf("--source *.pdf should keep only the pdf hit, got %+v", hits)
	}
}

func TestCLIMetadataWhereFilter(t *testing.T) {
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	seed := func(uri string, meta domain.Metadata) {
		did := domain.DeriveDocumentID("docs", uri)
		doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		doc.Metadata = meta
		ch, err := domain.NewChunk(did, 0, "content of "+uri)
		if err != nil {
			t.Fatal(err)
		}
		if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
			t.Fatal(err)
		}
		if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: qvec, Metadata: meta}}); err != nil {
			t.Fatal(err)
		}
	}
	seed("file:///a.md", domain.Metadata{"author": "alice", "date": "2025-06-01"})
	seed("file:///b.md", domain.Metadata{"author": "bob", "date": "2024-01-01"})

	t.Run("query --where filters by metadata and exposes it in --json", func(t *testing.T) {
		out, code := exec(deps, "query", "docs", "anything", "--where", "author=alice", "--json")
		if code != 0 {
			t.Fatalf("query exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(hits) != 1 || !strings.Contains(hits[0].Source, "a.md") {
			t.Fatalf("--where author=alice should keep only alice's hit, got %+v", hits)
		}
		if hits[0].Metadata["author"] != "alice" {
			t.Errorf("hit JSON should expose metadata, got %v", hits[0].Metadata)
		}
	})

	t.Run("query --where with a date predicate", func(t *testing.T) {
		out, code := exec(deps, "query", "docs", "anything", "--where", "date>=2025-01-01", "--json")
		if code != 0 {
			t.Fatalf("query exit %d", code)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if len(hits) != 1 || !strings.Contains(hits[0].Source, "a.md") {
			t.Errorf("date>=2025-01-01 should keep only a.md, got %+v", hits)
		}
	})

	t.Run("docs --where filters the document listing", func(t *testing.T) {
		out, code := exec(deps, "docs", "docs", "--where", "author=bob", "--json")
		if code != 0 {
			t.Fatalf("docs exit %d", code)
		}
		var list []docViewJSON
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if len(list) != 1 || !strings.Contains(list[0].Source, "b.md") || list[0].Metadata["author"] != "bob" {
			t.Errorf("docs --where author=bob should list only b.md with metadata, got %+v", list)
		}
	})

	t.Run("a malformed --where is a usage error (exit 2)", func(t *testing.T) {
		if _, code := exec(deps, "query", "docs", "anything", "--where", "author"); code != 2 {
			t.Errorf("malformed --where should exit 2, got %d", code)
		}
	})
}

func TestCLISpaceMismatchExits4(t *testing.T) {
	// Collection pinned to one space; embedder reports a different one.
	deps, colls, _, _ := newDeps(stubEmbedder{space: domain.EmbeddingSpace{Model: "other", Dimensions: 9}}, stubGenerator{})
	coll, err := domain.NewCollection("docs", testSpace(), testChunkerSpec(), time.Now())
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

func TestCLIChunkerMismatchExits4(t *testing.T) {
	ctx := context.Background()

	t.Run("collection pinned to a different chunker", func(t *testing.T) {
		deps, colls, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
		other, err := domain.NewChunkerSpec("structure", 1, 256, 32, "o200k_base", true) // differs from the deps' fixed chunker
		if err != nil {
			t.Fatal(err)
		}
		coll, err := domain.NewCollection("docs", testSpace(), other, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := colls.Create(ctx, coll); err != nil {
			t.Fatal(err)
		}
		if _, code := execStdin(deps, "some content", "add", "docs", "--stdin"); code != 4 {
			t.Errorf("want exit 4 (invariant), got %d", code)
		}
	})

	t.Run("legacy unpinned collection refuses ingest", func(t *testing.T) {
		deps, colls, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
		legacy := &domain.Collection{Name: "docs", Space: testSpace()} // zero chunker spec
		if err := colls.Create(ctx, legacy); err != nil {
			t.Fatal(err)
		}
		if _, code := execStdin(deps, "some content", "add", "docs", "--stdin"); code != 4 {
			t.Errorf("want exit 4 (invariant), got %d", code)
		}
	})
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
	if !ans.Grounded {
		t.Error("an answer over a seeded chunk should be grounded")
	}
	// Without --expand the JSON must be byte-for-byte unaffected: no expansions key.
	if strings.Contains(out, "expansions") {
		t.Errorf("--json without --expand must not include an expansions key: %s", out)
	}
	// Likewise --explain off: no explain key.
	if strings.Contains(out, "explain") {
		t.Errorf("--json without --explain must not include an explain key: %s", out)
	}
}

func TestCLIAskExpand(t *testing.T) {
	qvec := []float32{1, 0, 0}

	// Seeds two chunks with distinct vectors so retrieval order is deterministic:
	// d0 (alpha) cosine-matches the query vector, d1 (beta) less so.
	seed := func(t *testing.T, citeFirst int) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer", citeFirst: citeFirst})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		for i, spec := range []struct {
			body string
			vec  []float32
		}{
			{"alpha chunk body", []float32{1, 0, 0}},
			{"beta chunk body", []float32{0, 1, 0}},
		} {
			uri := fmt.Sprintf("file:///d%d.md", i)
			did := domain.DeriveDocumentID("docs", uri)
			doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ch, err := domain.NewChunk(did, 0, spec.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
				t.Fatal(err)
			}
			if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: spec.vec}}); err != nil {
				t.Fatal(err)
			}
		}
		return deps
	}

	t.Run("human output appends a Sources block with cited chunk text and ordinals", func(t *testing.T) {
		deps := seed(t, 0) // cite all retrieved
		out, code := exec(deps, "ask", "docs", "anything", "-k", "2", "--expand")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		if !strings.Contains(out, "Sources:") {
			t.Errorf("missing Sources block:\n%s", out)
		}
		if !strings.Contains(out, "[1]") || !strings.Contains(out, "[2]") {
			t.Errorf("Sources block should number with the answer's ordinals:\n%s", out)
		}
		if !strings.Contains(out, "alpha chunk body") || !strings.Contains(out, "beta chunk body") {
			t.Errorf("expand must include both cited chunks' full text:\n%s", out)
		}
	})

	t.Run("only cited chunks appear; an uncited retrieved chunk is absent", func(t *testing.T) {
		deps := seed(t, 1) // model cites only the first (d0/alpha) of two retrieved
		out, code := exec(deps, "ask", "docs", "anything", "-k", "2", "--expand")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(out, "alpha chunk body") {
			t.Errorf("cited chunk text should appear:\n%s", out)
		}
		if strings.Contains(out, "beta chunk body") {
			t.Errorf("uncited chunk text must not appear:\n%s", out)
		}
	})

	t.Run("--json adds an expansions array without disturbing existing fields", func(t *testing.T) {
		deps := seed(t, 0)
		out, code := exec(deps, "ask", "docs", "anything", "-k", "2", "--expand", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if ans.Text != "the answer" || len(ans.Citations) != 2 || !ans.Grounded {
			t.Errorf("existing fields disturbed: %+v", ans)
		}
		if len(ans.Expansions) != 2 {
			t.Fatalf("want 2 expansions, got %d", len(ans.Expansions))
		}
		texts := ans.Expansions[0].Text + "|" + ans.Expansions[1].Text
		if !strings.Contains(texts, "alpha chunk body") || !strings.Contains(texts, "beta chunk body") {
			t.Errorf("expansions missing chunk text: %+v", ans.Expansions)
		}
		// Each expansion uses the slice-1 chunk shape (chunk_id, seq, text).
		if ans.Expansions[0].ChunkID == "" {
			t.Errorf("expansion missing chunk_id: %+v", ans.Expansions[0])
		}
	})

	t.Run("ungrounded answer omits the block (and the json key)", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "ungrounded guess"})
		if _, code := exec(deps, "init", "empty"); code != 0 {
			t.Fatal("init failed")
		}
		out, errOut, code := execErr(deps, "ask", "empty", "anything", "--expand")
		if code != 0 {
			t.Fatalf("non-strict ungrounded should exit 0, got %d", code)
		}
		if strings.Contains(out, "Sources:") {
			t.Errorf("nothing cited: the Sources block must be omitted:\n%s", out)
		}
		if !strings.Contains(errOut, "not grounded") {
			t.Errorf("want the ungrounded warning on stderr, got %q", errOut)
		}
	})

	t.Run("--strict still errors before expansion", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "x"})
		if _, code := exec(deps, "init", "empty"); code != 0 {
			t.Fatal("init failed")
		}
		out, code := exec(deps, "ask", "empty", "anything", "--strict", "--expand")
		if code != 1 {
			t.Errorf("strict + ungrounded should exit 1, got %d", code)
		}
		if strings.Contains(out, "Sources:") {
			t.Errorf("strict must error before any expansion runs:\n%s", out)
		}
	})

	t.Run("--expand composes with --source scoping", func(t *testing.T) {
		deps := seed(t, 0)
		out, code := exec(deps, "ask", "docs", "anything", "-k", "2", "--source", "d0.md", "--expand")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		if !strings.Contains(out, "alpha chunk body") {
			t.Errorf("scoped expand should include the d0 chunk:\n%s", out)
		}
		if strings.Contains(out, "beta chunk body") {
			t.Errorf("--source d0.md should exclude d1's chunk:\n%s", out)
		}
	})
}

func TestCLIAskExplain(t *testing.T) {
	qvec := []float32{1, 0, 0}

	// Same deterministic seed as --expand: d0 (alpha) cosine-matches the query
	// (rank 1, high score), d1 (beta) is orthogonal (rank 2, ~0 score).
	seed := func(t *testing.T, citeFirst int) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer", citeFirst: citeFirst})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		for i, spec := range []struct {
			body string
			vec  []float32
		}{
			{"alpha chunk body", []float32{1, 0, 0}},
			{"beta chunk body", []float32{0, 1, 0}},
		} {
			uri := fmt.Sprintf("file:///d%d.md", i)
			did := domain.DeriveDocumentID("docs", uri)
			doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ch, err := domain.NewChunk(did, 0, spec.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
				t.Fatal(err)
			}
			if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: spec.vec}}); err != nil {
				t.Fatal(err)
			}
		}
		return deps
	}

	t.Run("--json carries explain inside the answer object, with cited + runner-up", func(t *testing.T) {
		deps := seed(t, 1) // model cites only the first (highest-scoring) hit
		out, code := exec(deps, "ask", "docs", "anything", "-k", "1", "--explain", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if ans.Text != "the answer" || len(ans.Citations) != 1 || !ans.Grounded {
			t.Errorf("existing fields disturbed: %+v", ans)
		}
		if ans.Explain == nil || len(ans.Explain.Returned) != 1 {
			t.Fatalf("want explain with 1 returned hit, got %+v", ans.Explain)
		}
		if c := ans.Explain.Returned[0].Cited; c == nil || !*c {
			t.Errorf("the single returned hit was cited; want cited=true, got %v", c)
		}
		if ans.Explain.NextScore == nil {
			t.Errorf("with 2 chunks and -k 1 there is a runner-up; next_score should be set")
		}
		if !strings.Contains(ans.Explain.Returned[0].Source, "d0.md") {
			t.Errorf("returned provenance wrong: %+v", ans.Explain.Returned[0])
		}
	})

	t.Run("human puts the explain block on stderr, keeping stdout the answer only", func(t *testing.T) {
		deps := seed(t, 1)
		out, errOut, code := execErr(deps, "ask", "docs", "anything", "-k", "2", "--explain")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(out, "the answer") || strings.Contains(out, "retrieval:") {
			t.Errorf("stdout should be just the answer, no explain block:\n%s", out)
		}
		if !strings.Contains(errOut, "retrieval:") || !strings.Contains(errOut, "d0.md") {
			t.Errorf("explain block should be on stderr:\n%s", errOut)
		}
		// d0 cited, d1 not (citeFirst=1) — both annotations present.
		if !strings.Contains(errOut, "cited") || !strings.Contains(errOut, "uncited") {
			t.Errorf("stderr should mark cited vs uncited hits:\n%s", errOut)
		}
	})

	t.Run("composes with --expand: Sources on stdout, explain on stderr", func(t *testing.T) {
		deps := seed(t, 0) // cite all
		out, errOut, code := execErr(deps, "ask", "docs", "anything", "-k", "1", "--expand", "--explain")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		if !strings.Contains(out, "Sources:") {
			t.Errorf("expand block should be on stdout:\n%s", out)
		}
		if !strings.Contains(errOut, "retrieval:") {
			t.Errorf("explain block should be on stderr:\n%s", errOut)
		}
	})

	t.Run("composes with --source: explain scoped to the matching document", func(t *testing.T) {
		deps := seed(t, 0)
		out, code := exec(deps, "ask", "docs", "anything", "-k", "2", "--source", "d0.md", "--explain", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if ans.Explain == nil || len(ans.Explain.Returned) != 1 || !strings.Contains(ans.Explain.Returned[0].Source, "d0.md") {
			t.Errorf("--source should scope explain to d0.md: %+v", ans.Explain)
		}
	})

	t.Run("--strict still errors before any explain output", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "x"})
		if _, code := exec(deps, "init", "empty"); code != 0 {
			t.Fatal("init failed")
		}
		out, errOut, code := execErr(deps, "ask", "empty", "anything", "--strict", "--explain")
		if code != 1 {
			t.Errorf("strict + ungrounded should exit 1, got %d", code)
		}
		if strings.Contains(out, "explain") || strings.Contains(errOut, "retrieval:") {
			t.Errorf("strict must error before any explain output:\nstdout=%s\nstderr=%s", out, errOut)
		}
	})
}

func TestCLIBudget(t *testing.T) {
	qvec := []float32{1, 0, 0}

	// Seed three equally-matching 3-word chunks so all are retrieved; the
	// word-count token counter then makes --budget arithmetic exact.
	seed := func(t *testing.T) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer"})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			uri := fmt.Sprintf("file:///d%d.md", i)
			doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ch, err := domain.NewChunk(doc.ID, 0, "aa bb cc") // 3 words = 3 tokens
			if err != nil {
				t.Fatal(err)
			}
			if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
				t.Fatal(err)
			}
			if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: qvec}}); err != nil {
				t.Fatal(err)
			}
		}
		return deps
	}

	t.Run("query --budget trims the returned set to the token cap", func(t *testing.T) {
		deps := seed(t)
		// Budget 7: 3 + 3 = 6 <= 7 keeps two; a third (9) would exceed.
		out, errOut, code := execErr(deps, "query", "docs", "anything", "--budget", "7", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(hits) != 2 {
			t.Errorf("budget 7 over 3-token chunks should return 2, got %d", len(hits))
		}
		if !strings.Contains(errOut, "returned 2 chunks") || !strings.Contains(errOut, "6 tokens") {
			t.Errorf("budget report missing/incorrect on stderr: %q", errOut)
		}
	})

	t.Run("ask --budget reports grounding tokens and caps grounding", func(t *testing.T) {
		deps := seed(t)
		out, code := exec(deps, "ask", "docs", "why?", "--budget", "7", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if ans.GroundingTokens == nil || *ans.GroundingTokens != 6 {
			t.Errorf("grounding_tokens = %v, want 6 (two 3-token chunks)", ans.GroundingTokens)
		}
	})

	t.Run("ask --json without --budget omits grounding_tokens", func(t *testing.T) {
		deps := seed(t)
		out, code := exec(deps, "ask", "docs", "why?", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if strings.Contains(out, "grounding_tokens") {
			t.Errorf("grounding_tokens should be omitted without --budget: %s", out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatal(err)
		}
		if ans.GroundingTokens != nil {
			t.Errorf("grounding_tokens should be nil without --budget, got %v", *ans.GroundingTokens)
		}
	})
}

func TestCLIQueryExplain(t *testing.T) {
	qvec := []float32{1, 0, 0}

	// d0 (alpha) cosine-matches the query; d1 (beta) is orthogonal — so order is
	// deterministic and there's a clear runner-up at -k 1.
	seed := func(t *testing.T) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		for i, spec := range []struct {
			body string
			vec  []float32
		}{
			{"alpha chunk body", []float32{1, 0, 0}},
			{"beta chunk body", []float32{0, 1, 0}},
		} {
			uri := fmt.Sprintf("file:///d%d.md", i)
			did := domain.DeriveDocumentID("docs", uri)
			doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ch, err := domain.NewChunk(did, 0, spec.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
				t.Fatal(err)
			}
			if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: spec.vec}}); err != nil {
				t.Fatal(err)
			}
		}
		return deps
	}

	t.Run("human: hits on stdout, score distribution on stderr with a runner-up", func(t *testing.T) {
		deps := seed(t)
		out, errOut, code := execErr(deps, "query", "docs", "anything", "-k", "1", "--explain")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(out, "alpha chunk body") || strings.Contains(out, "retrieval:") {
			t.Errorf("stdout should be the hits only:\n%s", out)
		}
		if !strings.Contains(errOut, "retrieval:") || !strings.Contains(errOut, "next candidate") {
			t.Errorf("explain distribution should be on stderr:\n%s", errOut)
		}
	})

	t.Run("--json: stdout stays the bare hit array; explain JSON on stderr", func(t *testing.T) {
		deps := seed(t)
		out, errOut, code := execErr(deps, "query", "docs", "anything", "-k", "1", "--explain", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		// stdout must remain exactly the hit-array contract (synthesize reads it).
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("stdout is not the bare hit array: %v\n%s", err, out)
		}
		if len(hits) != 1 || strings.Contains(out, "explain") {
			t.Errorf("stdout should be 1 hit and carry no explain key: %s", out)
		}
		// stderr carries the explain object with a runner-up next_score.
		var env struct {
			Explain explainViewJSON `json:"explain"`
		}
		if err := json.Unmarshal([]byte(errOut), &env); err != nil {
			t.Fatalf("stderr is not an explain JSON object: %v\n%s", err, errOut)
		}
		if len(env.Explain.Returned) != 1 || env.Explain.NextScore == nil {
			t.Errorf("explain should have 1 returned hit and a runner-up: %+v", env.Explain)
		}
		// query explain omits cited (no answer to cite from).
		if env.Explain.Returned[0].Cited != nil {
			t.Errorf("query explain must not carry a cited flag: %+v", env.Explain.Returned[0])
		}
	})

	t.Run("no runner-up when k covers every candidate", func(t *testing.T) {
		deps := seed(t)
		_, errOut, code := execErr(deps, "query", "docs", "anything", "-k", "5", "--explain", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		var env struct {
			Explain explainViewJSON `json:"explain"`
		}
		if err := json.Unmarshal([]byte(errOut), &env); err != nil {
			t.Fatalf("bad explain JSON: %v\n%s", err, errOut)
		}
		if env.Explain.NextScore != nil {
			t.Errorf("k=5 over 2 chunks: no runner-up, want next_score null, got %v", *env.Explain.NextScore)
		}
	})

	t.Run("no --explain: nothing on stderr", func(t *testing.T) {
		deps := seed(t)
		_, errOut, code := execErr(deps, "query", "docs", "anything", "-k", "1")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if strings.Contains(errOut, "retrieval:") || strings.Contains(errOut, "explain") {
			t.Errorf("without --explain stderr should be clean:\n%s", errOut)
		}
	})
}

func TestCLIAskGroundingGuard(t *testing.T) {
	// A collection that exists but holds no chunks: a query matches nothing.
	t.Run("strict refuses an ungrounded question (exit 1)", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{text: "ungrounded guess"})
		if _, code := exec(deps, "init", "empty"); code != 0 {
			t.Fatal("init failed")
		}
		out, _, code := execErr(deps, "ask", "empty", "anything", "--strict")
		if code != 1 {
			t.Errorf("strict + no grounding should exit 1, got %d", code)
		}
		if strings.Contains(out, "ungrounded guess") {
			t.Errorf("strict must not emit an answer, got stdout %q", out)
		}
	})

	t.Run("non-strict answers but warns on stderr", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{text: "ungrounded guess"})
		if _, code := exec(deps, "init", "empty"); code != 0 {
			t.Fatal("init failed")
		}
		out, errOut, code := execErr(deps, "ask", "empty", "anything")
		if code != 0 {
			t.Fatalf("non-strict should still answer (exit 0), got %d", code)
		}
		if !strings.Contains(out, "ungrounded guess") {
			t.Errorf("want the answer on stdout, got %q", out)
		}
		if !strings.Contains(errOut, "not grounded") {
			t.Errorf("want an ungrounded warning on stderr, got %q", errOut)
		}
	})
}

func TestCLISynthesize(t *testing.T) {
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer"})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
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

	t.Run("query --json piped into synthesize round-trips hits", func(t *testing.T) {
		hitsJSON, code := exec(deps, "query", "docs", "anything", "--json")
		if code != 0 {
			t.Fatalf("query exit %d", code)
		}
		out, code := execStdin(deps, hitsJSON, "synthesize", "why", "--json")
		if code != 0 {
			t.Fatalf("synthesize exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if ans.Text != "the answer" || !ans.Grounded {
			t.Errorf("answer = %+v", ans)
		}
		if len(ans.Citations) != 1 || ans.Citations[0].Source != "file:///a.md" || ans.Citations[0].Seq != 0 {
			t.Errorf("citation should round-trip from the piped hit: %+v", ans.Citations)
		}
	})

	t.Run("empty stdin yields an ungrounded answer", func(t *testing.T) {
		out, code := execStdin(deps, "", "synthesize", "why", "--json")
		if code != 0 {
			t.Fatalf("synthesize exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatal(err)
		}
		if ans.Grounded {
			t.Error("no piped hits should be ungrounded")
		}
	})

	t.Run("malformed stdin is a usage error", func(t *testing.T) {
		if _, code := execStdin(deps, "{not valid", "synthesize", "why"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})
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

func TestCLIHybridRetrieval(t *testing.T) {
	// The stub embedder returns the same vector for every chunk, so cosine ties —
	// only the BM25 lexical signal (populated through the real add path) can
	// distinguish a keyword match. Hybrid fusion must therefore surface it.
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fox.txt"), []byte("the quick brown fox jumps over the lazy dog"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lorem.txt"), []byte("lorem ipsum dolor sit amet consectetur"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	if _, code := exec(deps, "add", "docs", dir); code != 0 {
		t.Fatal("add failed")
	}

	t.Run("--hybrid surfaces the keyword match the vector tie would bury", func(t *testing.T) {
		out, code := exec(deps, "query", "docs", "fox", "--hybrid", "--json")
		if code != 0 {
			t.Fatalf("query --hybrid exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(hits) == 0 || !strings.Contains(hits[0].Source, "fox.txt") {
			t.Errorf("--hybrid should rank the keyword match (fox.txt) first, got %+v", hits)
		}
	})

	t.Run("--hybrid with multiple collections is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "query", "docs", "-c", "notes", "fox", "--hybrid"); code != 2 {
			t.Errorf("--hybrid + -c should exit 2, got %d", code)
		}
	})
}

func TestCLIDiversity(t *testing.T) {
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	// One document with three chunks (same source) plus a second document.
	seedDoc := func(uri string, nChunks int) {
		did := domain.DeriveDocumentID("docs", uri)
		doc, err := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var chunks []domain.Chunk
		var entries []app.VectorEntry
		for i := 0; i < nChunks; i++ {
			ch, err := domain.NewChunk(did, i, uri+" chunk")
			if err != nil {
				t.Fatal(err)
			}
			chunks = append(chunks, ch)
			entries = append(entries, app.VectorEntry{ChunkID: ch.ID, Vector: qvec})
		}
		if err := docs.Upsert(ctx, doc, chunks); err != nil {
			t.Fatal(err)
		}
		if err := index.Upsert(ctx, "docs", entries); err != nil {
			t.Fatal(err)
		}
	}
	seedDoc("file:///a.md", 3)
	seedDoc("file:///b.md", 1)

	t.Run("--max-per-source caps hits per document", func(t *testing.T) {
		out, code := exec(deps, "query", "docs", "anything", "-k", "8", "--max-per-source", "1", "--json")
		if code != 0 {
			t.Fatalf("query exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		perSource := map[string]int{}
		for _, h := range hits {
			perSource[h.Source]++
		}
		for src, n := range perSource {
			if n > 1 {
				t.Errorf("--max-per-source 1 should cap %s at 1, got %d", src, n)
			}
		}
		if len(hits) != 2 {
			t.Errorf("want 2 hits (1 per source), got %d", len(hits))
		}
	})

	t.Run("--mmr runs and returns diversified hits", func(t *testing.T) {
		out, code := exec(deps, "query", "docs", "anything", "--mmr", "--json")
		if code != 0 {
			t.Fatalf("query --mmr exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if len(hits) == 0 {
			t.Error("--mmr should return hits")
		}
	})

	t.Run("--mmr with --rerank is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "query", "docs", "anything", "--mmr", "--rerank"); code != 2 {
			t.Errorf("--mmr + --rerank should exit 2, got %d", code)
		}
	})

	t.Run("--mmr with multiple collections is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "query", "docs", "-c", "notes", "anything", "--mmr"); code != 2 {
			t.Errorf("--mmr + -c should exit 2, got %d", code)
		}
	})
}

func TestCLIAskVerify(t *testing.T) {
	c0 := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///a.md"), 0)
	qvec := []float32{1, 0, 0}
	// The canned answer cites c0 in its first sentence and leaves the second uncited.
	gen := stubGenerator{text: "The sky is blue [" + string(c0) + "]. An uncited sentence."}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, gen)
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	doc, _ := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Now())
	chunk, _ := domain.NewChunk(doc.ID, 0, "the sky is blue today")
	if err := docs.Upsert(ctx, doc, []domain.Chunk{chunk}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: chunk.ID, Vector: qvec}}); err != nil {
		t.Fatal(err)
	}

	type verifyJSON struct {
		SupportRate  *float64 `json:"support_rate"`
		Verification []struct {
			Claim       string   `json:"claim"`
			Verdict     string   `json:"verdict"`
			CitedChunks []string `json:"cited_chunks"`
		} `json:"verification"`
	}

	t.Run("--verify reports per-claim verdicts and a support rate", func(t *testing.T) {
		out, code := exec(deps, "ask", "docs", "q", "--verify", "--json")
		if code != 0 {
			t.Fatalf("ask --verify exit %d, out %q", code, out)
		}
		var v verifyJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(v.Verification) != 2 {
			t.Fatalf("want 2 claims, got %+v", v.Verification)
		}
		if v.Verification[0].Verdict != "supported" || v.Verification[1].Verdict != "uncited" {
			t.Errorf("verdicts = %q, %q", v.Verification[0].Verdict, v.Verification[1].Verdict)
		}
		if v.SupportRate == nil || *v.SupportRate != 0.5 {
			t.Errorf("support rate = %v, want 0.5", v.SupportRate)
		}
	})

	t.Run("--verify-strict exits 5 when a claim is unsupported", func(t *testing.T) {
		if _, code := exec(deps, "ask", "docs", "q", "--verify-strict"); code != 5 {
			t.Errorf("--verify-strict with an uncited claim should exit 5, got %d", code)
		}
	})
}

func TestCLIEval(t *testing.T) {
	c0 := domain.DeriveChunkID(domain.DeriveDocumentID("docs", "file:///a.md"), 0)
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "answer"})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	ctx := context.Background()
	doc, _ := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Now())
	chunk, _ := domain.NewChunk(doc.ID, 0, "auth uses keys")
	if err := docs.Upsert(ctx, doc, []domain.Chunk{chunk}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: chunk.ID, Vector: qvec}}); err != nil {
		t.Fatal(err)
	}
	jsonl := `{"version":1}` + "\n" + `{"question":"q","expected_chunks":["` + string(c0) + `"]}` + "\n"

	t.Run("reports aggregate metrics as JSON", func(t *testing.T) {
		out, code := execStdin(deps, jsonl, "eval", "docs", "--json")
		if code != 0 {
			t.Fatalf("eval exit %d, out %q", code, out)
		}
		var report struct {
			Aggregates map[string]float64 `json:"aggregates"`
		}
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if report.Aggregates["recall"] != 1 || report.Aggregates["hit_rate"] != 1 {
			t.Errorf("aggregates = %+v (want recall/hit 1)", report.Aggregates)
		}
	})

	t.Run("--fail-under passes when the metric meets the threshold", func(t *testing.T) {
		if _, code := execStdin(deps, jsonl, "eval", "docs", "--fail-under", "recall=0.5"); code != 0 {
			t.Errorf("recall 1.0 >= 0.5 should pass, got exit %d", code)
		}
	})

	t.Run("--fail-under exits 5 when the metric is below the threshold", func(t *testing.T) {
		if _, code := execStdin(deps, jsonl, "eval", "docs", "--fail-under", "recall=1.5"); code != 5 {
			t.Errorf("recall 1.0 < 1.5 should exit 5, got %d", code)
		}
	})

	t.Run("an unknown --fail-under metric is a usage error", func(t *testing.T) {
		if _, code := execStdin(deps, jsonl, "eval", "docs", "--fail-under", "bogus=0.5"); code != 2 {
			t.Errorf("unknown metric should exit 2, got %d", code)
		}
	})
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

func TestCLIStdinInput(t *testing.T) {
	qvec := []float32{1, 0, 0}

	t.Run("add --stdin ingests piped content, then it is queryable", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
		if _, code := exec(deps, "init", "kb"); code != 0 {
			t.Fatal("init failed")
		}
		out, code := execStdin(deps, "hello grounded world from a pipe", "add", "kb", "--stdin", "--json")
		if code != 0 {
			t.Fatalf("add --stdin exit %d, out %q", code, out)
		}
		var sum ingestViewJSON
		if err := json.Unmarshal([]byte(out), &sum); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if sum.Added != 1 || sum.Chunks < 1 {
			t.Errorf("add --stdin summary = %+v", sum)
		}

		hits, code := exec(deps, "query", "kb", "anything", "--json")
		if code != 0 {
			t.Fatalf("query exit %d", code)
		}
		var hv []hitViewJSON
		if err := json.Unmarshal([]byte(hits), &hv); err != nil {
			t.Fatal(err)
		}
		if len(hv) < 1 || !strings.Contains(hv[0].Text, "hello grounded world") {
			t.Errorf("piped content not retrievable: %+v", hv)
		}
	})

	t.Run("--stdin rejects path arguments", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
		exec(deps, "init", "kb")
		if _, code := execStdin(deps, "x", "add", "kb", "--stdin", "some/path"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("ask and query read text from stdin when the arg is -", func(t *testing.T) {
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer"})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		doc, _ := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Now())
		chunk, _ := domain.NewChunk(doc.ID, 0, "the grounded answer")
		if err := docs.Upsert(ctx, doc, []domain.Chunk{chunk}); err != nil {
			t.Fatal(err)
		}
		if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: chunk.ID, Vector: qvec}}); err != nil {
			t.Fatal(err)
		}

		out, code := execStdin(deps, "why does it work?\n", "ask", "docs", "-", "--json")
		if code != 0 {
			t.Fatalf("ask - exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatal(err)
		}
		if ans.Text != "the answer" {
			t.Errorf("ask - answer = %+v", ans)
		}

		out, code = execStdin(deps, "anything\n", "query", "docs", "-", "--json")
		if code != 0 {
			t.Fatalf("query - exit %d", code)
		}
		var hv []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hv); err != nil {
			t.Fatal(err)
		}
		if len(hv) < 1 {
			t.Errorf("query - returned no hits")
		}
	})
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

func TestCLIRemoveChunk(t *testing.T) {
	qvec := []float32{1, 0, 0}
	docID := domain.DeriveDocumentID("docs", "file:///a.md")

	seed := func(t *testing.T) (cli.Deps, domain.Chunk, domain.Chunk) {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		doc, err := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("a")), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		c0, _ := domain.NewChunk(docID, 0, "first chunk body")
		c1, _ := domain.NewChunk(docID, 1, "second chunk body")
		if err := docs.Upsert(ctx, doc, []domain.Chunk{c0, c1}); err != nil {
			t.Fatal(err)
		}
		for _, c := range []domain.Chunk{c0, c1} {
			if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: c.ID, Vector: qvec}}); err != nil {
				t.Fatal(err)
			}
		}
		return deps, c0, c1
	}

	t.Run("removes the named chunk, leaving the document and its other chunks", func(t *testing.T) {
		deps, c0, c1 := seed(t)
		out, code := exec(deps, "rm", "docs", "--chunk", string(c1.ID), "--json")
		if code != 0 {
			t.Fatalf("rm --chunk exit %d, out %q", code, out)
		}
		var v rmViewJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if v.Removed != "chunks" || v.Collection != "docs" || len(v.ChunkIDs) != 1 || v.ChunkIDs[0] != string(c1.ID) {
			t.Errorf("rm view = %+v", v)
		}

		// The document survives with only its remaining chunk.
		catOut, code := exec(deps, "cat", "docs", "--doc", "file:///a.md", "--json")
		if code != 0 {
			t.Fatalf("cat exit %d", code)
		}
		var chunks []chunkViewJSON
		if err := json.Unmarshal([]byte(catOut), &chunks); err != nil {
			t.Fatal(err)
		}
		if len(chunks) != 1 || chunks[0].ChunkID != string(c0.ID) {
			t.Errorf("want only c0 left, got %+v", chunks)
		}
	})

	t.Run("--chunk and --doc together is a usage error", func(t *testing.T) {
		deps, c0, _ := seed(t)
		if _, code := exec(deps, "rm", "docs", "--doc", "file:///a.md", "--chunk", string(c0.ID)); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("malformed chunk ID is a usage error", func(t *testing.T) {
		deps, _, _ := seed(t)
		if _, code := exec(deps, "rm", "docs", "--chunk", "not-a-valid-id"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("partial miss removes the found, warns on stderr, exits 3", func(t *testing.T) {
		deps, c0, _ := seed(t)
		ghost := string(domain.DeriveChunkID(docID, 99))
		out, errOut, code := execErr(deps, "rm", "docs", "--chunk", string(c0.ID), "--chunk", ghost, "--json")
		if code != 3 {
			t.Fatalf("want exit 3, got %d", code)
		}
		var v rmViewJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(v.ChunkIDs) != 1 || v.ChunkIDs[0] != string(c0.ID) {
			t.Errorf("the found chunk should still be removed: %+v", v)
		}
		if !strings.Contains(errOut, ghost) || !strings.Contains(errOut, "not found") {
			t.Errorf("want a per-ID warning on stderr, got %q", errOut)
		}
	})

	t.Run("unknown collection exits 3", func(t *testing.T) {
		deps, c0, _ := seed(t)
		if _, code := exec(deps, "rm", "ghostcoll", "--chunk", string(c0.ID)); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})
}

func TestCLIRerankCommand(t *testing.T) {
	qvec := []float32{1, 0, 0}
	// Two hits as the `query --json` array would carry them.
	stdin := `[{"chunk_id":"aaaa:0","source":"file:///a.md","seq":0,"score":0.9,"text":"alpha"},` +
		`{"chunk_id":"bbbb:0","source":"file:///b.md","seq":0,"score":0.2,"text":"beta"}]`

	t.Run("reorders stdin hits by rerank score and adds rerank_score", func(t *testing.T) {
		deps := withReranker(mustInitDeps(t, qvec), &stubRerankProvider{})
		out, code := execStdin(deps, stdin, "rerank", "the query", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		// Stub reverses input order: beta first now, alpha second.
		if len(hits) != 2 || hits[0].ChunkID != "bbbb:0" || hits[1].ChunkID != "aaaa:0" {
			t.Errorf("rerank did not reorder: %+v", hits)
		}
		if hits[0].RerankScore == nil || *hits[0].RerankScore <= *hits[1].RerankScore {
			t.Errorf("rerank_score missing or not descending: %+v", hits)
		}
		// Original similarity score preserved alongside.
		if hits[0].Score != 0.2 {
			t.Errorf("similarity score lost: %v", hits[0].Score)
		}
	})

	t.Run("-n truncates after reranking", func(t *testing.T) {
		deps := withReranker(mustInitDeps(t, qvec), &stubRerankProvider{})
		out, code := execStdin(deps, stdin, "rerank", "q", "-n", "1", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ChunkID != "bbbb:0" {
			t.Errorf("want top-1 after rerank (beta), got %+v", hits)
		}
	})

	t.Run("empty stdin yields empty output, exit 0, no provider call", func(t *testing.T) {
		prov := &stubRerankProvider{}
		deps := withReranker(mustInitDeps(t, qvec), prov)
		out, code := execStdin(deps, "", "rerank", "q", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if strings.TrimSpace(out) != "[]" {
			t.Errorf("want empty array, got %q", out)
		}
		if prov.called {
			t.Error("provider must not be called for empty input")
		}
	})

	t.Run("malformed stdin is a usage error", func(t *testing.T) {
		deps := withReranker(mustInitDeps(t, qvec), &stubRerankProvider{})
		if _, code := execStdin(deps, "{not valid", "rerank", "q"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("unconfigured rerank provider is a usage error", func(t *testing.T) {
		deps := mustInitDeps(t, qvec) // no reranker wired
		if _, code := execStdin(deps, stdin, "rerank", "q"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("provider failure exits 1 with nothing on stdout", func(t *testing.T) {
		deps := withReranker(mustInitDeps(t, qvec), &stubRerankProvider{err: errors.New("rerank 500")})
		out, code := execStdin(deps, stdin, "rerank", "q", "--json")
		if code != 1 {
			t.Errorf("want exit 1, got %d", code)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("stdout must be empty on failure, got %q", out)
		}
	})
}

func TestCLIQueryRerank(t *testing.T) {
	qvec := []float32{1, 0, 0}

	// alpha cosine-matches the query (high sim); beta is orthogonal (low sim).
	seed := func(t *testing.T, prov app.RerankProvider) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		for i, spec := range []struct {
			body string
			vec  []float32
		}{
			{"alpha chunk body", []float32{1, 0, 0}},
			{"beta chunk body", []float32{0, 1, 0}},
		} {
			uri := fmt.Sprintf("file:///d%d.md", i)
			did := domain.DeriveDocumentID("docs", uri)
			doc, _ := domain.NewDocument("docs", uri, domain.HashContent([]byte(uri)), time.Now())
			ch, _ := domain.NewChunk(did, 0, spec.body)
			if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
				t.Fatal(err)
			}
			if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: ch.ID, Vector: spec.vec}}); err != nil {
				t.Fatal(err)
			}
		}
		if prov != nil {
			deps = withReranker(deps, prov)
		}
		return deps
	}

	t.Run("two-stage: retrieves the pool, reranks, returns -k reordered", func(t *testing.T) {
		deps := seed(t, &stubRerankProvider{}) // reverses vector order
		out, code := exec(deps, "query", "docs", "anything", "-k", "1", "--rerank", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatal(err)
		}
		// Vector order would return d0 (alpha); rerank reverses, so the top-1 is d1.
		if len(hits) != 1 || !strings.Contains(hits[0].Source, "d1.md") {
			t.Errorf("rerank should reorder the vector result: %+v", hits)
		}
		if hits[0].RerankScore == nil {
			t.Errorf("reranked hit should carry rerank_score: %+v", hits[0])
		}
	})

	t.Run("--rerank-candidates < -k is a usage error", func(t *testing.T) {
		deps := seed(t, &stubRerankProvider{})
		if _, code := exec(deps, "query", "docs", "q", "-k", "5", "--rerank", "--rerank-candidates", "2"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("unconfigured rerank provider is a usage error", func(t *testing.T) {
		deps := seed(t, nil) // no reranker
		if _, code := exec(deps, "query", "docs", "q", "--rerank"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("--explain over --rerank shows rerank scores on stderr", func(t *testing.T) {
		deps := seed(t, &stubRerankProvider{})
		_, errOut, code := execErr(deps, "query", "docs", "anything", "-k", "1", "--rerank", "--explain")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(errOut, "rerank=") {
			t.Errorf("explain over rerank should show rerank scores:\n%s", errOut)
		}
	})
}

func TestCLIAskRerank(t *testing.T) {
	qvec := []float32{1, 0, 0}
	seed := func(t *testing.T) cli.Deps {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "the answer"})
		if _, code := exec(deps, "init", "docs"); code != 0 {
			t.Fatal("init failed")
		}
		ctx := context.Background()
		doc, _ := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Now())
		chunk, _ := domain.NewChunk(doc.ID, 0, "the grounded answer")
		if err := docs.Upsert(ctx, doc, []domain.Chunk{chunk}); err != nil {
			t.Fatal(err)
		}
		if err := index.Upsert(ctx, "docs", []app.VectorEntry{{ChunkID: chunk.ID, Vector: qvec}}); err != nil {
			t.Fatal(err)
		}
		return withReranker(deps, &stubRerankProvider{})
	}

	t.Run("two-stage retrieval feeds synthesis", func(t *testing.T) {
		deps := seed(t)
		out, code := exec(deps, "ask", "docs", "why?", "-k", "1", "--rerank", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatal(err)
		}
		if ans.Text != "the answer" || !ans.Grounded || len(ans.Citations) != 1 {
			t.Errorf("rerank ask answer wrong: %+v", ans)
		}
	})

	t.Run("composes with --explain (rerank scores in the answer's explain)", func(t *testing.T) {
		deps := seed(t)
		out, code := exec(deps, "ask", "docs", "why?", "-k", "1", "--rerank", "--explain", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatal(err)
		}
		if ans.Explain == nil || len(ans.Explain.Returned) != 1 || ans.Explain.Returned[0].RerankScore == nil {
			t.Errorf("explain should carry a rerank score: %+v", ans.Explain)
		}
	})

	t.Run("unconfigured rerank provider is a usage error", func(t *testing.T) {
		deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "x"})
		exec(deps, "init", "docs")
		if _, code := exec(deps, "ask", "docs", "q", "--rerank"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})
}

// seedChunk ingests one chunk + its vector into a collection through the ports,
// for cross-collection retrieval tests.
func seedChunk(t *testing.T, docs *memstore.DocumentRepository, index *memstore.VectorIndex, collection, uri string, seq int, text string, vec []float32) domain.Chunk {
	t.Helper()
	ctx := context.Background()
	did := domain.DeriveDocumentID(collection, uri)
	doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(uri+text)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := domain.NewChunk(did, seq, text)
	if err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, doc, []domain.Chunk{ch}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, collection, []app.VectorEntry{{ChunkID: ch.ID, Vector: vec}}); err != nil {
		t.Fatal(err)
	}
	return ch
}

// seedDoc ingests one document with several chunks (one per text/vec pair) into
// a collection, so chunks sharing a source URI live under one document rather
// than colliding on document ID.
func seedDoc(t *testing.T, docs *memstore.DocumentRepository, index *memstore.VectorIndex, collection, uri string, texts []string, vecs [][]float32) []domain.Chunk {
	t.Helper()
	ctx := context.Background()
	did := domain.DeriveDocumentID(collection, uri)
	doc, err := domain.NewDocument(collection, uri, domain.HashContent([]byte(uri)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]domain.Chunk, len(texts))
	entries := make([]app.VectorEntry, len(texts))
	for i := range texts {
		ch, err := domain.NewChunk(did, i, texts[i])
		if err != nil {
			t.Fatal(err)
		}
		chunks[i] = ch
		entries[i] = app.VectorEntry{ChunkID: ch.ID, Vector: vecs[i]}
	}
	if err := docs.Upsert(ctx, doc, chunks); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, collection, entries); err != nil {
		t.Fatal(err)
	}
	return chunks
}

func TestCLIQueryFromCollection(t *testing.T) {
	// Source v1 and target v2 share the embedder's space. The stub embedder is
	// never consulted on this path (vectors come from the index), so its vec is
	// irrelevant.
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
	for _, c := range []string{"v1", "v2"} {
		if _, code := exec(deps, "init", c); code != 0 {
			t.Fatalf("init %s exit %d", c, code)
		}
	}
	src := seedDoc(t, docs, index, "v1", "file:///v1.md",
		[]string{"source zero", "source one"}, [][]float32{{1, 0, 0}, {0, 1, 0}})
	sc0, sc1 := src[0], src[1]
	tgt := seedDoc(t, docs, index, "v2", "file:///v2.md",
		[]string{"target zero", "target one"}, [][]float32{{1, 0, 0}, {0, 1, 0}})
	tc0, tc1 := tgt[0], tgt[1]

	t.Run("groups each source chunk's target hits as JSON", func(t *testing.T) {
		out, code := exec(deps, "query", "v2", "--from-collection", "v1", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var groups []fromGroupViewJSON
		if err := json.Unmarshal([]byte(out), &groups); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(groups) != 2 {
			t.Fatalf("want 2 groups, got %d: %+v", len(groups), groups)
		}
		if groups[0].From.ChunkID != string(sc0.ID) || groups[0].From.Source != "file:///v1.md" {
			t.Errorf("group[0].from = %+v, want %s", groups[0].From, sc0.ID)
		}
		if len(groups[0].Hits) == 0 || groups[0].Hits[0].ChunkID != string(tc0.ID) {
			t.Errorf("group[0] best hit = %+v, want %s", groups[0].Hits, tc0.ID)
		}
		if groups[1].From.ChunkID != string(sc1.ID) || groups[1].Hits[0].ChunkID != string(tc1.ID) {
			t.Errorf("group[1] = from %s hits %+v, want from %s best %s", groups[1].From.ChunkID, groups[1].Hits, sc1.ID, tc1.ID)
		}
	})

	t.Run("-k bounds the hits per group", func(t *testing.T) {
		out, code := exec(deps, "query", "v2", "--from-collection", "v1", "-k", "1", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var groups []fromGroupViewJSON
		if err := json.Unmarshal([]byte(out), &groups); err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if len(g.Hits) != 1 {
				t.Errorf("-k 1 should bound each group to one hit, got %d", len(g.Hits))
			}
		}
	})

	t.Run("--from-collection with positional query text is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "query", "v2", "extra text", "--from-collection", "v1"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("--from-collection with stdin - is a usage error", func(t *testing.T) {
		if _, code := execStdin(deps, "piped", "query", "v2", "-", "--from-collection", "v1"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("unknown source or target collection exits 3", func(t *testing.T) {
		if _, code := exec(deps, "query", "v2", "--from-collection", "ghost"); code != 3 {
			t.Errorf("unknown source: want exit 3, got %d", code)
		}
		if _, code := exec(deps, "query", "ghost", "--from-collection", "v1"); code != 3 {
			t.Errorf("unknown target: want exit 3, got %d", code)
		}
	})

	t.Run("mismatched source/target spaces exits 4", func(t *testing.T) {
		deps, colls, docs, index := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
		v2, err := domain.NewCollection("v2", testSpace(), testChunkerSpec(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		v1, err := domain.NewCollection("v1", domain.EmbeddingSpace{Model: "other", Dimensions: 9}, testChunkerSpec(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := colls.Create(ctx, v1); err != nil {
			t.Fatal(err)
		}
		if err := colls.Create(ctx, v2); err != nil {
			t.Fatal(err)
		}
		_ = docs
		_ = index
		if _, code := exec(deps, "query", "v2", "--from-collection", "v1"); code != 4 {
			t.Errorf("want exit 4 (invariant), got %d", code)
		}
	})
}

func TestCLIMultiCollection(t *testing.T) {
	qvec := []float32{1, 0, 0}
	setup := func(t *testing.T) (cli.Deps, domain.Chunk, domain.Chunk) {
		t.Helper()
		deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{text: "merged answer"})
		for _, c := range []string{"a", "b"} {
			if _, code := exec(deps, "init", c); code != 0 {
				t.Fatalf("init %s failed", c)
			}
		}
		// a0 is a perfect match for the query; b0 is a weaker match — so the merge
		// orders a0 above b0 across collections.
		a0 := seedChunk(t, docs, index, "a", "file:///a.md", 0, "alpha", []float32{1, 0, 0})
		b0 := seedChunk(t, docs, index, "b", "file:///b.md", 0, "beta", []float32{0.8, 0.2, 0})
		return deps, a0, b0
	}

	t.Run("query -c a -c b merges and tags each hit's collection", func(t *testing.T) {
		deps, a0, b0 := setup(t)
		out, code := exec(deps, "query", "-c", "a", "-c", "b", "anything", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if len(hits) != 2 {
			t.Fatalf("want 2 merged hits, got %d", len(hits))
		}
		if hits[0].ChunkID != string(a0.ID) || hits[0].Collection != "a" {
			t.Errorf("hit[0] = %+v, want %s from a", hits[0], a0.ID)
		}
		if hits[1].ChunkID != string(b0.ID) || hits[1].Collection != "b" {
			t.Errorf("hit[1] = %+v, want %s from b", hits[1], b0.ID)
		}
	})

	t.Run("ask -c a -c b answers with per-collection citations", func(t *testing.T) {
		deps, _, _ := setup(t)
		out, code := exec(deps, "ask", "-c", "a", "-c", "b", "how?", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var ans answerViewJSON
		if err := json.Unmarshal([]byte(out), &ans); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if !ans.Grounded || len(ans.Citations) != 2 {
			t.Fatalf("want 2 grounded citations, got %+v", ans)
		}
		got := map[string]bool{}
		for _, c := range ans.Citations {
			got[c.Collection] = true
		}
		if !got["a"] || !got["b"] {
			t.Errorf("citations should span both collections, got %+v", ans.Citations)
		}
	})

	t.Run("a single -c behaves like the positional (no collection tag)", func(t *testing.T) {
		deps, a0, _ := setup(t)
		out, code := exec(deps, "query", "-c", "a", "anything", "--json")
		if code != 0 {
			t.Fatalf("exit %d, out %q", code, out)
		}
		var hits []hitViewJSON
		if err := json.Unmarshal([]byte(out), &hits); err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ChunkID != string(a0.ID) {
			t.Fatalf("want a0 only, got %+v", hits)
		}
		if hits[0].Collection != "" {
			t.Errorf("single-collection query should not tag a collection, got %q", hits[0].Collection)
		}
	})

	t.Run("mismatched spaces across collections exits 4 with no retrieval", func(t *testing.T) {
		deps, colls, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
		a, _ := domain.NewCollection("a", testSpace(), testChunkerSpec(), time.Now())
		b, _ := domain.NewCollection("b", domain.EmbeddingSpace{Model: "other", Dimensions: 9}, testChunkerSpec(), time.Now())
		ctx := context.Background()
		if err := colls.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := colls.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, code := exec(deps, "query", "-c", "a", "-c", "b", "q"); code != 4 {
			t.Errorf("query: want exit 4, got %d", code)
		}
		if _, code := exec(deps, "ask", "-c", "a", "-c", "b", "q"); code != 4 {
			t.Errorf("ask: want exit 4, got %d", code)
		}
	})

	t.Run("--from-collection combined with -c is a usage error", func(t *testing.T) {
		deps, _, _ := setup(t)
		if _, code := exec(deps, "query", "v2", "--from-collection", "a", "-c", "b"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})
}

// transferViewJSON mirrors the export/import summary JSON.
type transferViewJSON struct {
	Collection string `json:"collection"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	Documents  int    `json:"documents"`
	Chunks     int    `json:"chunks"`
	Encrypted  bool   `json:"encrypted"`
	Output     string `json:"output"`
}

func TestCLIExportImport(t *testing.T) {
	qvec := []float32{1, 0, 0}
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
	if _, code := exec(deps, "init", "kb"); code != 0 {
		t.Fatal("init failed")
	}
	seedDoc(t, docs, index, "kb", "file:///a.md",
		[]string{"the grounded answer", "second chunk here"}, [][]float32{{1, 0, 0}, {0, 1, 0}})
	file := filepath.Join(t.TempDir(), "kb.lore")

	t.Run("export writes a summary and a file", func(t *testing.T) {
		out, code := exec(deps, "export", "kb", "-o", file, "--json")
		if code != 0 {
			t.Fatalf("export exit %d, out %q", code, out)
		}
		var v transferViewJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("bad JSON %q: %v", out, err)
		}
		if v.Collection != "kb" || v.Documents != 1 || v.Chunks != 2 || v.Encrypted {
			t.Errorf("export summary = %+v", v)
		}
		if fi, err := os.Stat(file); err != nil || fi.Size() == 0 {
			t.Fatalf("artifact not written: %v", err)
		}
	})

	t.Run("import reconstructs losslessly and query matches the original", func(t *testing.T) {
		out, code := exec(deps, "import", file, "--name", "kb2", "--json")
		if code != 0 {
			t.Fatalf("import exit %d, out %q", code, out)
		}
		var v transferViewJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatal(err)
		}
		if v.Collection != "kb2" || v.Documents != 1 || v.Chunks != 2 {
			t.Errorf("import summary = %+v", v)
		}
		// A query against the imported collection returns the same top hit (text +
		// score) as the original — the vectors and pins round-tripped.
		orig, _ := exec(deps, "query", "kb", "anything", "-k", "1", "--json")
		imp, _ := exec(deps, "query", "kb2", "anything", "-k", "1", "--json")
		var oh, ih []hitViewJSON
		_ = json.Unmarshal([]byte(orig), &oh)
		_ = json.Unmarshal([]byte(imp), &ih)
		if len(oh) != 1 || len(ih) != 1 {
			t.Fatalf("expected one hit each, got %d and %d", len(oh), len(ih))
		}
		if oh[0].Text != ih[0].Text || oh[0].Score != ih[0].Score {
			t.Errorf("query mismatch after import: orig %+v, imported %+v", oh[0], ih[0])
		}
	})

	t.Run("importing over an existing name is refused without --force", func(t *testing.T) {
		if _, code := exec(deps, "import", file); code == 0 {
			t.Error("importing over existing 'kb' should fail")
		}
	})

	t.Run("--force replaces", func(t *testing.T) {
		if _, code := exec(deps, "import", file, "--force"); code != 0 {
			t.Errorf("--force import should succeed, got exit %d", code)
		}
	})

	t.Run("missing -o is a usage error", func(t *testing.T) {
		if _, code := exec(deps, "export", "kb"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("export of unknown collection exits 3", func(t *testing.T) {
		if _, code := exec(deps, "export", "ghost", "-o", filepath.Join(t.TempDir(), "x.lore")); code != 3 {
			t.Errorf("want exit 3, got %d", code)
		}
	})

	t.Run("importing a newer artifact version is a clear error, nothing imported", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "future.lore")
		buf := append([]byte(artifact.Magic), 0, 0, 0, byte(artifact.FormatVersion+1))
		buf = append(buf, []byte("body")...)
		if err := os.WriteFile(bad, buf, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, code := exec(deps, "import", bad, "--name", "future"); code == 0 {
			t.Error("importing a newer version should fail")
		}
		if _, code := exec(deps, "status", "future"); code != 3 {
			t.Error("nothing should have been imported")
		}
	})
}

func TestCLIExportImportEncrypted(t *testing.T) {
	deps, _, docs, index := newDeps(stubEmbedder{space: testSpace(), vec: []float32{1, 0, 0}}, stubGenerator{})
	if _, code := exec(deps, "init", "kb"); code != 0 {
		t.Fatal("init failed")
	}
	seedDoc(t, docs, index, "kb", "file:///a.md",
		[]string{"the grounded answer"}, [][]float32{{1, 0, 0}})

	// An age key pair for the recipient/identity path (no shell needed).
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(idFile, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("recipient/identity round-trips and the artifact reveals nothing in clear", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "kb.lore.age")
		if _, code := execStdin(deps, "", "export", "kb", "-o", file, "--encrypt", "--recipient", id.Recipient().String()); code != 0 {
			t.Fatalf("encrypted export failed, exit %d", code)
		}
		raw, _ := os.ReadFile(file)
		if !agecrypt.IsEncrypted(raw) {
			t.Error("artifact should be age-encrypted")
		}
		// Needles must be long enough not to collide with age's random
		// nonce/ephemeral-key bytes: a 2-byte name like "kb" turns up in random
		// ciphertext by chance. The name is encrypted inside the bundle alongside
		// the content, so the longer needles below already prove it cannot leak.
		for _, leak := range [][]byte{[]byte("the grounded answer"), []byte(artifact.Magic), []byte(testSpace().Model)} {
			if bytes.Contains(raw, leak) {
				t.Errorf("encrypted artifact leaks %q in clear", leak)
			}
		}
		if _, code := execStdin(deps, "", "import", file, "--name", "kb_r", "--identity", idFile); code != 0 {
			t.Fatalf("identity import failed, exit %d", code)
		}
		if _, code := exec(deps, "status", "kb_r"); code != 0 {
			t.Error("imported collection should exist")
		}
	})

	t.Run("tampered ciphertext fails to decrypt (exit 1), nothing imported", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "t.lore.age")
		if _, code := execStdin(deps, "", "export", "kb", "-o", file, "--encrypt", "--recipient", id.Recipient().String()); code != 0 {
			t.Fatal("export failed")
		}
		raw, _ := os.ReadFile(file)
		raw[len(raw)-1] ^= 0xff
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, code := execStdin(deps, "", "import", file, "--name", "kb_t", "--identity", idFile); code != 1 {
			t.Errorf("tampered import: want exit 1, got %d", code)
		}
		if _, code := exec(deps, "status", "kb_t"); code != 3 {
			t.Error("nothing should be imported from a tampered artifact")
		}
	})

	t.Run("wrong identity fails (exit 1)", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "w.lore.age")
		execStdin(deps, "", "export", "kb", "-o", file, "--encrypt", "--recipient", id.Recipient().String())
		other, _ := age.GenerateX25519Identity()
		otherFile := filepath.Join(t.TempDir(), "other.txt")
		_ = os.WriteFile(otherFile, []byte(other.String()), 0o600)
		if _, code := execStdin(deps, "", "import", file, "--name", "kb_w", "--identity", otherFile); code != 1 {
			t.Errorf("wrong identity: want exit 1, got %d", code)
		}
	})

	t.Run("--encrypt with no key source and no TTY is a usage error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "n.lore.age")
		if _, code := execStdin(deps, "", "export", "kb", "-o", file, "--encrypt"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("encrypted artifact with no key source and no TTY is a usage error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "e.lore.age")
		execStdin(deps, "", "export", "kb", "-o", file, "--encrypt", "--recipient", id.Recipient().String())
		if _, code := execStdin(deps, "", "import", file, "--name", "kb_e"); code != 2 {
			t.Errorf("want exit 2 (detected encrypted, no key), got %d", code)
		}
	})

	t.Run("passphrase and recipient together is a usage error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "x.lore.age")
		if _, code := execStdin(deps, "", "export", "kb", "-o", file, "--encrypt",
			"--recipient", id.Recipient().String(), "--passphrase-cmd", "echo hi"); code != 2 {
			t.Errorf("want exit 2, got %d", code)
		}
	})

	t.Run("passphrase-cmd round-trips", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("passphrase-cmd test assumes a POSIX shell")
		}
		file := filepath.Join(t.TempDir(), "p.lore.age")
		if _, code := execStdin(deps, "", "export", "kb", "-o", file, "--encrypt", "--passphrase-cmd", "printf hunter2"); code != 0 {
			t.Fatalf("passphrase export failed, exit %d", code)
		}
		if _, code := execStdin(deps, "", "import", file, "--name", "kb_pp", "--passphrase-cmd", "printf hunter2"); code != 0 {
			t.Fatalf("passphrase import failed, exit %d", code)
		}
		// Wrong passphrase → exit 1.
		if _, code := execStdin(deps, "", "import", file, "--name", "kb_pp2", "--passphrase-cmd", "printf wrong"); code != 1 {
			t.Errorf("wrong passphrase: want exit 1, got %d", code)
		}
	})
}

// mustInitDeps builds deps with an initialized "docs" collection, for rerank
// command tests that only need stdin hits (no seeded vectors).
func mustInitDeps(t *testing.T, qvec []float32) cli.Deps {
	t.Helper()
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace(), vec: qvec}, stubGenerator{})
	if _, code := exec(deps, "init", "docs"); code != 0 {
		t.Fatal("init failed")
	}
	return deps
}

// Mirror of the CLI's JSON output shapes, for decoding in tests.
type collectionViewJSON struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  string `json:"created_at"`
}

type docViewJSON struct {
	Source     string            `json:"source"`
	Hash       string            `json:"hash"`
	IngestedAt string            `json:"ingested_at"`
	Metadata   map[string]string `json:"metadata"`
}

type statusViewJSON struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  string `json:"created_at"`
	Documents  int    `json:"documents"`
}

type syncViewJSON struct {
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Unsupported int      `json:"unsupported"`
	Chunks      int      `json:"chunks"`
	Pruned      int      `json:"pruned"`
	PrunedURIs  []string `json:"pruned_uris"`
	DryRun      bool     `json:"dry_run"`
}

type chunkViewJSON struct {
	ChunkID string `json:"chunk_id"`
	Seq     int    `json:"seq"`
	Text    string `json:"text"`
}

type hitViewJSON struct {
	ChunkID     string            `json:"chunk_id"`
	Source      string            `json:"source"`
	Seq         int               `json:"seq"`
	Score       float64           `json:"score"`
	RerankScore *float64          `json:"rerank_score"`
	Collection  string            `json:"collection"`
	Metadata    map[string]string `json:"metadata"`
	Text        string            `json:"text"`
}

type fromGroupViewJSON struct {
	From struct {
		ChunkID string `json:"chunk_id"`
		Source  string `json:"source"`
		Seq     int    `json:"seq"`
	} `json:"from"`
	Hits []hitViewJSON `json:"hits"`
}

type answerViewJSON struct {
	Text      string `json:"text"`
	Citations []struct {
		ChunkID    string `json:"chunk_id"`
		Source     string `json:"source"`
		Seq        int    `json:"seq"`
		Collection string `json:"collection"`
	} `json:"citations"`
	Grounded        bool             `json:"grounded"`
	Expansions      []chunkViewJSON  `json:"expansions"`
	Explain         *explainViewJSON `json:"explain"`
	GroundingTokens *int             `json:"grounding_tokens"`
}

type explainViewJSON struct {
	Returned []struct {
		Score       float64  `json:"score"`
		RerankScore *float64 `json:"rerank_score"`
		Source      string   `json:"source"`
		Seq         int      `json:"seq"`
		Cited       *bool    `json:"cited"`
	} `json:"returned"`
	NextScore *float64 `json:"next_score"`
	Stats     struct {
		Min  float64 `json:"min"`
		Max  float64 `json:"max"`
		Mean float64 `json:"mean"`
	} `json:"stats"`
}

type ingestViewJSON struct {
	Added       int `json:"added"`
	Skipped     int `json:"skipped"`
	Unsupported int `json:"unsupported"`
	Chunks      int `json:"chunks"`
}

type rmViewJSON struct {
	Removed    string   `json:"removed"`
	Collection string   `json:"collection"`
	Document   string   `json:"document"`
	ChunkIDs   []string `json:"chunk_ids"`
}
