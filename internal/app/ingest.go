package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jmurray2011/lore/internal/domain"
)

// DefaultIngestConcurrency bounds how many documents are processed at once.
const DefaultIngestConcurrency = 8

// IngestSummary reports the outcome of an ingestion run.
type IngestSummary struct {
	Added       int // documents ingested (new or changed)
	Skipped     int // unchanged or empty documents (idempotent no-ops)
	Unsupported int // documents whose content type no extractor handles
	Excluded    int // documents skipped by an --exclude glob (never read)
	Chunks      int // chunks embedded and stored
}

// Ingestor walks a Source, extracts and chunks each document, embeds the
// chunks, and stores chunks and their vectors. Ingestion is idempotent:
// a source whose extracted content is unchanged is a no-op.
type Ingestor struct {
	collections CollectionRepository
	docs        DocumentRepository
	index       VectorIndex
	lexical     LexicalIndex
	embedder    Embedder
	extractor   Extractor
	source      Source
	chunkers    domain.Registry
	concurrency int
	now         func() time.Time
}

// IngestOption configures an Ingestor at construction.
type IngestOption func(*Ingestor)

// WithConcurrency sets the bounded parallelism for ingestion. Values <= 0 are
// ignored, leaving DefaultIngestConcurrency in place. Lower it for providers
// with tight rate limits to avoid retry thrash.
func WithConcurrency(n int) IngestOption {
	return func(i *Ingestor) {
		if n > 0 {
			i.concurrency = n
		}
	}
}

// ingestCall holds per-invocation ingest configuration.
type ingestCall struct {
	meta    domain.Metadata
	exclude []string // glob patterns; a matching source is skipped before it is read
}

// isExcluded reports whether a source URI matches any --exclude glob. A glob is
// matched against both the document's basename (the common case, e.g. "*(1).pdf")
// and the full URI (so path-shaped globs like "*/drafts/*" work). A malformed
// pattern never matches here; the CLI rejects those up front as a usage error.
func (c ingestCall) isExcluded(uri string) bool {
	base := path.Base(uri)
	for _, g := range c.exclude {
		if ok, err := path.Match(g, base); err == nil && ok {
			return true
		}
		if ok, err := path.Match(g, uri); err == nil && ok {
			return true
		}
	}
	return false
}

// IngestCallOption configures a single Ingest or IngestContent invocation (as
// distinct from IngestOption, which configures the Ingestor at construction).
type IngestCallOption func(*ingestCall)

// WithMeta supplies base metadata applied to every document ingested in this
// invocation (the `add --meta` pairs). A markdown document's own front matter is
// merged under it, so an explicit key here overrides the same key from front
// matter. Metadata is captured only when a document is ingested as new or
// changed; re-ingesting unchanged content does not update it.
func WithMeta(meta domain.Metadata) IngestCallOption {
	return func(c *ingestCall) { c.meta = meta }
}

// WithExclude skips any source whose URI matches one of the given globs, before
// it is read, extracted, or embedded. Excluded sources are reported in the
// summary's Excluded count, never silently dropped. It applies to walking a path
// (add <dir>); it has no effect on IngestContent (stdin yields a single named
// document with nothing to filter). Empty patterns are ignored.
func WithExclude(globs ...string) IngestCallOption {
	return func(c *ingestCall) {
		for _, g := range globs {
			if g != "" {
				c.exclude = append(c.exclude, g)
			}
		}
	}
}

func newIngestCall(opts []IngestCallOption) ingestCall {
	var c ingestCall
	for _, o := range opts {
		o(&c)
	}
	return c
}

// NewIngestor wires an Ingestor from the ports and domain services it needs. The
// chunker Registry selects a per-format chunking strategy by content type.
func NewIngestor(collections CollectionRepository, docs DocumentRepository, index VectorIndex, embedder Embedder, extractor Extractor, source Source, chunkers domain.Registry, lexical LexicalIndex, opts ...IngestOption) *Ingestor {
	ing := &Ingestor{
		collections: collections,
		docs:        docs,
		index:       index,
		lexical:     lexical,
		embedder:    embedder,
		extractor:   extractor,
		source:      source,
		chunkers:    chunkers,
		concurrency: DefaultIngestConcurrency,
		now:         time.Now,
	}
	for _, opt := range opts {
		opt(ing)
	}
	return ing
}

// Ingest processes every document the Source yields under root into the named
// collection, concurrently with bounded parallelism. It enforces space
// coherence up front and fails fast on the first error; because
// ingestion is idempotent, re-running resumes safely over already-stored
// documents.
func (i *Ingestor) Ingest(ctx context.Context, collection, root string, opts ...IngestCallOption) (IngestSummary, error) {
	call := newIngestCall(opts)
	coll, err := i.collections.Get(ctx, collection)
	if err != nil {
		return IngestSummary{}, err
	}

	space, err := i.embedder.Space(ctx)
	if err != nil {
		return IngestSummary{}, fmt.Errorf("embedder space: %w", err)
	}
	if err := coll.AcceptsSpace(space); err != nil {
		return IngestSummary{}, err
	}
	if err := coll.AcceptsChunker(i.chunkers.Spec()); err != nil {
		return IngestSummary{}, err
	}

	var added, skipped, unsupported, excluded, chunks atomic.Int64

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(i.concurrency)

	walkErr := i.source.Walk(gctx, root, func(it SourceItem) error {
		if call.isExcluded(it.URI) {
			excluded.Add(1)
			return gctx.Err()
		}
		g.Go(func() error {
			out, err := i.ingestItem(gctx, coll, it, call.meta)
			if err != nil {
				return err
			}
			switch out.kind {
			case kindAdded:
				added.Add(1)
				chunks.Add(int64(out.chunks))
			case kindUnsupported:
				unsupported.Add(1)
			default: // kindSkipped
				skipped.Add(1)
			}
			return nil
		})
		return gctx.Err() // stop walking promptly once a worker has failed
	})

	if err := g.Wait(); err != nil {
		return IngestSummary{}, err
	}
	if walkErr != nil {
		return IngestSummary{}, fmt.Errorf("walk %q: %w", root, walkErr)
	}

	// Remember the root so `lore sync` can replay it without a path argument.
	if err := i.collections.RecordSource(ctx, collection, root); err != nil {
		return IngestSummary{}, fmt.Errorf("record source %q: %w", root, err)
	}

	return IngestSummary{
		Added:       int(added.Load()),
		Skipped:     int(skipped.Load()),
		Unsupported: int(unsupported.Load()),
		Excluded:    int(excluded.Load()),
		Chunks:      int(chunks.Load()),
	}, nil
}

// ingestKind classifies a single document's outcome.
type ingestKind int

const (
	kindAdded       ingestKind = iota // new or changed: chunks embedded and stored
	kindSkipped                       // unchanged or empty: nothing to (re)ingest
	kindUnsupported                   // content type handled by no extractor
)

// IngestContent ingests a single in-memory document (e.g. read from stdin) into
// the collection, identified by uri with the given content type. Like Ingest it
// enforces space coherence and is idempotent by content hash. Unlike Ingest it
// records no sync source — there is no path to
// replay — and an unsupported content type is reported, not an error.
func (i *Ingestor) IngestContent(ctx context.Context, collection, uri, contentType string, content []byte, opts ...IngestCallOption) (IngestSummary, error) {
	call := newIngestCall(opts)
	coll, err := i.collections.Get(ctx, collection)
	if err != nil {
		return IngestSummary{}, err
	}
	space, err := i.embedder.Space(ctx)
	if err != nil {
		return IngestSummary{}, fmt.Errorf("embedder space: %w", err)
	}
	if err := coll.AcceptsSpace(space); err != nil {
		return IngestSummary{}, err
	}
	if err := coll.AcceptsChunker(i.chunkers.Spec()); err != nil {
		return IngestSummary{}, err
	}

	// Empty fingerprint: stdin has no cheap source-side signature, so fast-skip
	// is disabled; content-hash idempotency still applies once content is read.
	item := SourceItem{
		URI:         uri,
		ContentType: contentType,
		Open:        func() ([]byte, error) { return content, nil },
	}
	out, err := i.ingestItem(ctx, coll, item, call.meta)
	if err != nil {
		return IngestSummary{}, err
	}

	sum := IngestSummary{Chunks: out.chunks}
	switch out.kind {
	case kindAdded:
		sum.Added = 1
	case kindUnsupported:
		sum.Unsupported = 1
	default:
		sum.Skipped = 1
	}
	return sum, nil
}

type ingestOutcome struct {
	chunks int
	kind   ingestKind
}

// ingestItem processes a single source item: idempotency check, extract, chunk,
// embed, then store. Vectors are upserted before the document record so the
// stored document acts as the commit marker — a failure before it leaves the
// document absent (a re-run reprocesses), and any vectors written without a
// document are harmless: queries skip chunk IDs the DocumentRepository can't
// hydrate.
func (i *Ingestor) ingestItem(ctx context.Context, coll *domain.Collection, item SourceItem, baseMeta domain.Metadata) (ingestOutcome, error) {
	if !i.extractor.Supports(item.ContentType) {
		return ingestOutcome{kind: kindUnsupported}, nil
	}

	var existing *domain.Document
	switch d, err := i.docs.GetBySource(ctx, coll.Name, item.URI); {
	case err == nil:
		existing = d
		// Fast path: a matching fingerprint means the source is unchanged, so we
		// skip without reading, extracting, or embedding.
		if item.Fingerprint != "" && existing.Fingerprint == item.Fingerprint {
			return ingestOutcome{kind: kindSkipped}, nil
		}
	case errors.Is(err, ErrNotFound):
		// new document
	default:
		return ingestOutcome{}, fmt.Errorf("lookup %q: %w", item.URI, err)
	}

	raw, err := item.Open()
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("read %q: %w", item.URI, err)
	}
	text, err := i.extractor.Extract(item.ContentType, raw)
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("extract %q: %w", item.URI, err)
	}
	// Markdown front matter is metadata, not prose: parse it, merge the caller's
	// base metadata over it (an explicit --meta key wins), and chunk only the body.
	docMeta := baseMeta.Clone()
	if isMarkdown(item.ContentType) {
		var fm domain.Metadata
		fm, text = domain.ParseFrontMatter(text)
		docMeta = mergeMeta(fm, docMeta)
	}
	// Record the source's filesystem modify time as recency provenance, for every
	// content type. An explicit author-supplied value (front matter or --meta) wins.
	if !item.ModTime.IsZero() {
		if docMeta == nil {
			docMeta = domain.Metadata{}
		}
		if _, ok := docMeta[domain.MetaKeyModTime]; !ok {
			docMeta[domain.MetaKeyModTime] = item.ModTime.UTC().Format(time.RFC3339)
		}
	}
	hash := domain.HashContent([]byte(text))
	results, err := i.chunkers.Chunk(domain.ParsedDoc{Text: text, ContentType: item.ContentType, SourceURI: item.URI})
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("chunk %q: %w", item.URI, err)
	}
	texts := embedTexts(results)

	if existing != nil && existing.Unchanged(hash) {
		// Content is unchanged but the fingerprint drifted (or was never recorded,
		// e.g. a document ingested before fingerprints). Refresh it so the next
		// sync fast-skips. The deterministic chunker yields the same chunks, so
		// re-storing leaves the vectors valid and avoids re-embedding.
		if item.Fingerprint != "" && existing.Fingerprint != item.Fingerprint && len(results) > 0 {
			if err := i.refreshFingerprint(ctx, coll, existing, item.Fingerprint, results); err != nil {
				return ingestOutcome{}, err
			}
		}
		return ingestOutcome{kind: kindSkipped}, nil
	}

	if len(results) == 0 {
		return ingestOutcome{kind: kindSkipped}, nil
	}

	var prior *domain.Document
	if existing != nil {
		prior = existing // changed: its old chunks/vectors are replaced below
	}

	doc, err := domain.NewDocument(coll.Name, item.URI, hash, i.now())
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("document %q: %w", item.URI, err)
	}
	doc.Fingerprint = item.Fingerprint
	doc.Metadata = docMeta
	chunks, err := chunksFor(doc.ID, results, item.URI)
	if err != nil {
		return ingestOutcome{}, err
	}

	vectors, err := i.embedder.Embed(ctx, texts)
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("embed %q: %w", item.URI, err)
	}
	if len(vectors) != len(chunks) {
		return ingestOutcome{}, fmt.Errorf("embedder returned %d vectors for %d chunks of %q", len(vectors), len(chunks), item.URI)
	}
	// Each entry carries the document's metadata so the index can apply a --where
	// filter without reaching into the DocumentRepository.
	entries := make([]VectorEntry, len(chunks))
	for j, ch := range chunks {
		entries[j] = VectorEntry{ChunkID: ch.ID, Vector: vectors[j], Metadata: docMeta}
	}

	// For a changed document, drop the prior version's chunks and vectors before
	// writing the new ones — otherwise a shrunk document leaves orphaned tail
	// vectors in the index. Embedding happened first, so a failure
	// there leaves the prior version intact; deleting here means a failure before
	// the document is re-stored just reprocesses on the next run.
	if prior != nil {
		stale, err := i.docs.Delete(ctx, coll.Name, prior.ID)
		if err != nil {
			return ingestOutcome{}, fmt.Errorf("replace %q: %w", item.URI, err)
		}
		if err := i.index.Delete(ctx, coll.Name, stale); err != nil {
			return ingestOutcome{}, fmt.Errorf("replace %q: %w", item.URI, err)
		}
		if i.lexical != nil {
			if err := i.lexical.Delete(ctx, coll.Name, stale); err != nil {
				return ingestOutcome{}, fmt.Errorf("replace %q: %w", item.URI, err)
			}
		}
	}

	if err := i.index.Upsert(ctx, coll.Name, entries); err != nil {
		return ingestOutcome{}, fmt.Errorf("index %q: %w", item.URI, err)
	}
	if i.lexical != nil {
		if err := i.lexical.Upsert(ctx, coll.Name, lexicalDocs(chunks, docMeta)); err != nil {
			return ingestOutcome{}, fmt.Errorf("lexical index %q: %w", item.URI, err)
		}
	}
	if err := i.docs.Upsert(ctx, doc, chunks); err != nil {
		return ingestOutcome{}, fmt.Errorf("store %q: %w", item.URI, err)
	}

	return ingestOutcome{chunks: len(chunks), kind: kindAdded}, nil
}

// refreshFingerprint re-stores an unchanged document with a new fingerprint,
// preserving its hash, ingestion time, and (deterministically re-derived)
// chunks, so the next sync can fast-skip it. Vectors are untouched: identical
// text yields identical chunk IDs.
func (i *Ingestor) refreshFingerprint(ctx context.Context, coll *domain.Collection, existing *domain.Document, fingerprint string, results []domain.ChunkResult) error {
	doc, err := domain.NewDocument(coll.Name, existing.SourceURI, existing.Hash, existing.IngestedAt)
	if err != nil {
		return fmt.Errorf("refresh %q: %w", existing.SourceURI, err)
	}
	doc.Fingerprint = fingerprint
	chunks, err := chunksFor(doc.ID, results, existing.SourceURI)
	if err != nil {
		return err
	}
	if err := i.docs.Upsert(ctx, doc, chunks); err != nil {
		return fmt.Errorf("refresh %q: %w", existing.SourceURI, err)
	}
	return nil
}

// isMarkdown reports whether content of this type carries markdown front matter.
func isMarkdown(contentType string) bool {
	return strings.HasPrefix(contentType, "text/markdown")
}

// mergeMeta overlays b onto a (b wins on key conflict), returning a fresh map, or
// nil when both are empty.
func mergeMeta(a, b domain.Metadata) domain.Metadata {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(domain.Metadata, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// lexicalDocs builds the lexical-index documents for a set of chunks, indexing
// each chunk's stored text under the document's metadata (so --where filters the
// lexical side identically to the vector side).
func lexicalDocs(chunks []domain.Chunk, meta domain.Metadata) []LexicalDoc {
	docs := make([]LexicalDoc, len(chunks))
	for i, c := range chunks {
		docs[i] = LexicalDoc{ChunkID: c.ID, Text: c.Text, Metadata: meta}
	}
	return docs
}

// embedTexts pulls the text to embed from each chunk result, in order. A chunk
// may embed text that differs from what is stored (e.g. a heading-path context
// prefix); the stored Text always remains the original (see chunksFor).
func embedTexts(results []domain.ChunkResult) []string {
	texts := make([]string, len(results))
	for i, r := range results {
		texts[i] = r.TextToEmbed()
	}
	return texts
}

// chunksFor builds the domain Chunks for a document's chunk results, carrying
// each chunk's section metadata.
func chunksFor(docID domain.DocumentID, results []domain.ChunkResult, uri string) ([]domain.Chunk, error) {
	chunks := make([]domain.Chunk, len(results))
	for seq, r := range results {
		ch, err := domain.NewChunk(docID, seq, r.Text)
		if err != nil {
			return nil, fmt.Errorf("chunk %d of %q: %w", seq, uri, err)
		}
		ch.HeadingPath = r.HeadingPath
		chunks[seq] = ch
	}
	return chunks, nil
}
