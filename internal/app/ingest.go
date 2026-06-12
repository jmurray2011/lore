package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jmurray2011/lore/internal/domain"
)

// DefaultIngestConcurrency bounds how many documents are processed at once.
const DefaultIngestConcurrency = 8

// IngestSummary reports the outcome of an ingestion run.
type IngestSummary struct {
	Added   int // documents ingested (new or changed)
	Skipped int // unchanged, unsupported, or empty documents
	Chunks  int // chunks embedded and stored
}

// Ingestor walks a Source, extracts and chunks each document, embeds the
// chunks, and stores chunks and their vectors. Ingestion is idempotent
// (invariant 2): a source whose extracted content is unchanged is a no-op.
type Ingestor struct {
	collections CollectionRepository
	docs        DocumentRepository
	index       VectorIndex
	embedder    Embedder
	extractor   Extractor
	source      Source
	chunker     domain.Chunker
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

// NewIngestor wires an Ingestor from the ports and domain services it needs.
func NewIngestor(collections CollectionRepository, docs DocumentRepository, index VectorIndex, embedder Embedder, extractor Extractor, source Source, chunker domain.Chunker, opts ...IngestOption) *Ingestor {
	ing := &Ingestor{
		collections: collections,
		docs:        docs,
		index:       index,
		embedder:    embedder,
		extractor:   extractor,
		source:      source,
		chunker:     chunker,
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
// coherence up front (invariant 1) and fails fast on the first error; because
// ingestion is idempotent, re-running resumes safely over already-stored
// documents.
func (i *Ingestor) Ingest(ctx context.Context, collection, root string) (IngestSummary, error) {
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

	var added, skipped, chunks atomic.Int64

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(i.concurrency)

	walkErr := i.source.Walk(gctx, root, func(it SourceItem) error {
		g.Go(func() error {
			out, err := i.ingestItem(gctx, coll, it)
			if err != nil {
				return err
			}
			if out.skipped {
				skipped.Add(1)
			} else {
				added.Add(1)
				chunks.Add(int64(out.chunks))
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
		Added:   int(added.Load()),
		Skipped: int(skipped.Load()),
		Chunks:  int(chunks.Load()),
	}, nil
}

type ingestOutcome struct {
	chunks  int
	skipped bool
}

// ingestItem processes a single source item: extract, idempotency check, chunk,
// embed, then store. Vectors are upserted before the document record so the
// stored document acts as the commit marker — a failure before it leaves the
// document absent (a re-run reprocesses), and any vectors written without a
// document are harmless: queries skip chunk IDs the DocumentRepository can't
// hydrate.
func (i *Ingestor) ingestItem(ctx context.Context, coll *domain.Collection, item SourceItem) (ingestOutcome, error) {
	if !i.extractor.Supports(item.ContentType) {
		return ingestOutcome{skipped: true}, nil
	}
	text, err := i.extractor.Extract(item.ContentType, item.Content)
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("extract %q: %w", item.URI, err)
	}
	hash := domain.HashContent([]byte(text))

	var prior *domain.Document
	switch existing, err := i.docs.GetBySource(ctx, coll.Name, item.URI); {
	case err == nil:
		if existing.Unchanged(hash) {
			return ingestOutcome{skipped: true}, nil
		}
		prior = existing // changed: its old chunks/vectors are replaced below
	case errors.Is(err, ErrNotFound):
		// new document
	default:
		return ingestOutcome{}, fmt.Errorf("lookup %q: %w", item.URI, err)
	}

	texts := i.chunker.Split(text)
	if len(texts) == 0 {
		return ingestOutcome{skipped: true}, nil
	}

	doc, err := domain.NewDocument(coll.Name, item.URI, hash, i.now())
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("document %q: %w", item.URI, err)
	}
	chunks := make([]domain.Chunk, len(texts))
	for seq, t := range texts {
		ch, err := domain.NewChunk(doc.ID, seq, t)
		if err != nil {
			return ingestOutcome{}, fmt.Errorf("chunk %d of %q: %w", seq, item.URI, err)
		}
		chunks[seq] = ch
	}

	vectors, err := i.embedder.Embed(ctx, texts)
	if err != nil {
		return ingestOutcome{}, fmt.Errorf("embed %q: %w", item.URI, err)
	}
	if len(vectors) != len(chunks) {
		return ingestOutcome{}, fmt.Errorf("embedder returned %d vectors for %d chunks of %q", len(vectors), len(chunks), item.URI)
	}
	entries := make([]VectorEntry, len(chunks))
	for j, ch := range chunks {
		entries[j] = VectorEntry{ChunkID: ch.ID, Vector: vectors[j]}
	}

	// For a changed document, drop the prior version's chunks and vectors before
	// writing the new ones — otherwise a shrunk document leaves orphaned tail
	// vectors in the index (invariant 3). Embedding happened first, so a failure
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
	}

	if err := i.index.Upsert(ctx, coll.Name, entries); err != nil {
		return ingestOutcome{}, fmt.Errorf("index %q: %w", item.URI, err)
	}
	if err := i.docs.Upsert(ctx, doc, chunks); err != nil {
		return ingestOutcome{}, fmt.Errorf("store %q: %w", item.URI, err)
	}

	return ingestOutcome{chunks: len(chunks)}, nil
}
