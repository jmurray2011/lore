package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/jmurray2011/lore/internal/artifact"
	"github.com/jmurray2011/lore/internal/domain"
)

// TransferSummary describes a collection that was exported or imported.
type TransferSummary struct {
	Collection string
	Model      string
	Dimensions int
	Documents  int
	Chunks     int
}

// Exporter serializes a collection into a portable artifact — its embedding-space
// and chunker pins, metadata, documents, chunks, and vectors — gathered through
// the persistence ports so the format stays independent of the storage backend.
type Exporter struct {
	collections CollectionRepository
	docs        DocumentRepository
	index       VectorIndex
}

// NewExporter wires an Exporter over the three persistence ports.
func NewExporter(collections CollectionRepository, docs DocumentRepository, index VectorIndex) *Exporter {
	return &Exporter{collections: collections, docs: docs, index: index}
}

// Export writes the named collection to w as a self-contained, versioned
// artifact. The embedding-space pin (and chunker pin, if the collection carries
// one) travel inside it so the reconstructed collection enforces the same
// invariants. An unknown collection is ErrNotFound. The bytes written are the
// plaintext artifact; encryption, if any, wraps this stream in the caller.
func (e *Exporter) Export(ctx context.Context, collection string, w io.Writer) (TransferSummary, error) {
	coll, err := e.collections.Get(ctx, collection)
	if err != nil {
		return TransferSummary{}, err
	}
	docs, err := e.docs.ListDocuments(ctx, collection)
	if err != nil {
		return TransferSummary{}, fmt.Errorf("list documents: %w", err)
	}
	entries, err := e.index.Entries(ctx, collection)
	if err != nil {
		return TransferSummary{}, fmt.Errorf("read vectors: %w", err)
	}
	vecByID := make(map[domain.ChunkID][]float32, len(entries))
	for _, en := range entries {
		vecByID[en.ChunkID] = en.Vector
	}

	bundle := artifact.Bundle{Collection: artifact.Collection{
		Name:       coll.Name,
		Model:      coll.Space.Model,
		Dimensions: coll.Space.Dimensions,
		CreatedAt:  coll.CreatedAt,
		Sources:    coll.Sources,
		Chunker: artifact.Chunker{
			Strategy:      coll.Chunker.Strategy,
			Version:       coll.Chunker.Version,
			Size:          coll.Chunker.Size,
			Overlap:       coll.Chunker.Overlap,
			Tokenizer:     coll.Chunker.Tokenizer,
			ContextPrefix: coll.Chunker.ContextPrefix,
		},
	}}

	// Stable document order keeps the artifact deterministic across backends.
	sort.Slice(docs, func(i, j int) bool { return docs[i].SourceURI < docs[j].SourceURI })
	chunkCount := 0
	for _, d := range docs {
		chunks, err := e.docs.GetChunksByDocument(ctx, collection, d.ID)
		if err != nil {
			return TransferSummary{}, fmt.Errorf("read chunks of %q: %w", d.SourceURI, err)
		}
		ad := artifact.Document{
			SourceURI:   d.SourceURI,
			Hash:        string(d.Hash),
			IngestedAt:  d.IngestedAt,
			Fingerprint: d.Fingerprint,
			Metadata:    map[string]string(d.Metadata),
		}
		for _, c := range chunks {
			vec, ok := vecByID[c.ID]
			if !ok {
				return TransferSummary{}, fmt.Errorf("chunk %s has no vector in the index; collection is not exportable", c.ID)
			}
			ad.Chunks = append(ad.Chunks, artifact.Chunk{
				Seq:         c.Seq,
				Text:        c.Text,
				HeadingPath: c.HeadingPath,
				Vector:      vec,
			})
			chunkCount++
		}
		bundle.Documents = append(bundle.Documents, ad)
	}

	if err := artifact.Write(w, bundle); err != nil {
		return TransferSummary{}, err
	}
	return TransferSummary{
		Collection: coll.Name,
		Model:      coll.Space.Model,
		Dimensions: coll.Space.Dimensions,
		Documents:  len(docs),
		Chunks:     chunkCount,
	}, nil
}

// Importer reconstructs a collection from a portable artifact, writing it into
// the local store through the persistence ports. The chunk and document IDs are
// re-derived from the (target collection, source URI, seq) rather than carried,
// so importing under a new name produces a coherent collection.
type Importer struct {
	collections CollectionRepository
	docs        DocumentRepository
	index       VectorIndex
	lexical     LexicalIndex
	remover     *Remover
	embedder    Embedder
}

// NewImporter wires an Importer; the Remover handles the cascade when --force
// replaces an existing collection. The lexical index is rebuilt from the imported
// chunks (it is derived content, not carried in the artifact); a nil lexical index
// imports without one. The embedder is used only by re-embed imports (rebuilding
// vectors in the local space); a nil embedder makes re-embed a usage error.
func NewImporter(collections CollectionRepository, docs DocumentRepository, index VectorIndex, remover *Remover, lexical LexicalIndex, embedder Embedder) *Importer {
	return &Importer{collections: collections, docs: docs, index: index, lexical: lexical, remover: remover, embedder: embedder}
}

// Import reconstructs the collection in r (a plaintext artifact; any decryption
// has already happened upstream). A non-empty name renames it. A target name
// that already exists is refused with ErrAlreadyExists unless force, which
// replaces it via the cascade. A malformed or too-new artifact surfaces the
// artifact package's errors before anything is written.
func (im *Importer) Import(ctx context.Context, r io.Reader, name string, force, reEmbed bool) (TransferSummary, error) {
	b, err := artifact.Read(r)
	if err != nil {
		return TransferSummary{}, err
	}

	target := b.Collection.Name
	if name != "" {
		target = name
	}
	if err := domain.ValidateCollectionName(target); err != nil {
		return TransferSummary{}, err
	}
	// The collection is pinned to the artifact's embedding space, unless re-embed
	// rebuilds the vectors in the local embedder's space — the path that makes a
	// handed corpus queryable when you cannot serve the model it was built with.
	space, err := domain.NewEmbeddingSpace(b.Collection.Model, b.Collection.Dimensions)
	if err != nil {
		return TransferSummary{}, fmt.Errorf("artifact embedding space: %w", err)
	}
	if reEmbed {
		if im.embedder == nil {
			return TransferSummary{}, fmt.Errorf("%w: --re-embed needs an embedder, none is configured", domain.ErrInvalidArgument)
		}
		local, serr := im.embedder.Space(ctx)
		if serr != nil {
			return TransferSummary{}, fmt.Errorf("re-embed: read embedder space: %w", serr)
		}
		space = local
	}
	chunker := domain.ChunkerSpec{
		Strategy:      b.Collection.Chunker.Strategy,
		Version:       b.Collection.Chunker.Version,
		Size:          b.Collection.Chunker.Size,
		Overlap:       b.Collection.Chunker.Overlap,
		Tokenizer:     b.Collection.Chunker.Tokenizer,
		ContextPrefix: b.Collection.Chunker.ContextPrefix,
	}
	// A zero spec is a legacy (pre-pin) collection, imported read-only; a non-zero
	// spec must be well-formed.
	if !chunker.IsZero() {
		if err := chunker.Validate(); err != nil {
			return TransferSummary{}, fmt.Errorf("artifact chunker: %w", err)
		}
	}

	switch _, err := im.collections.Get(ctx, target); {
	case err == nil:
		if !force {
			return TransferSummary{}, fmt.Errorf("collection %q already exists; use --force to overwrite: %w", target, ErrAlreadyExists)
		}
		if err := im.remover.RemoveCollection(ctx, target); err != nil {
			return TransferSummary{}, fmt.Errorf("overwrite %q: %w", target, err)
		}
	case errors.Is(err, ErrNotFound):
		// fresh import
	default:
		return TransferSummary{}, err
	}

	coll := &domain.Collection{Name: target, Space: space, Chunker: chunker, CreatedAt: b.Collection.CreatedAt}
	if err := im.collections.Create(ctx, coll); err != nil {
		return TransferSummary{}, err
	}
	for _, s := range b.Collection.Sources {
		if err := im.collections.RecordSource(ctx, target, s); err != nil {
			return TransferSummary{}, fmt.Errorf("record source: %w", err)
		}
	}

	chunkCount := 0
	for _, d := range b.Documents {
		docID := domain.DeriveDocumentID(target, d.SourceURI)
		meta := domain.Metadata(d.Metadata)
		doc := &domain.Document{
			ID:          docID,
			Collection:  target,
			SourceURI:   d.SourceURI,
			Hash:        domain.ContentHash(d.Hash),
			IngestedAt:  d.IngestedAt,
			Fingerprint: d.Fingerprint,
			Metadata:    meta,
		}
		chunks := make([]domain.Chunk, len(d.Chunks))
		for i, c := range d.Chunks {
			chunks[i] = domain.Chunk{ID: domain.DeriveChunkID(docID, c.Seq), DocumentID: docID, Seq: c.Seq, Text: c.Text, HeadingPath: c.HeadingPath}
		}
		// Vectors are the carried ones, or — with re-embed — rebuilt from the chunk
		// text in the local space. Re-embed uses the stored chunk text (any
		// heading-prefix embedding context is not reconstructed); the vectors are
		// mutually coherent and match a query embedded by the same embedder, which
		// is what retrieval needs. A purist re-ingests from source.
		vectors := make([][]float32, len(d.Chunks))
		if reEmbed {
			texts := make([]string, len(chunks))
			for i, c := range chunks {
				texts[i] = c.Text
			}
			vecs, eerr := im.embedder.Embed(ctx, texts)
			if eerr != nil {
				return TransferSummary{}, fmt.Errorf("re-embed %q: %w", d.SourceURI, eerr)
			}
			if len(vecs) != len(chunks) {
				return TransferSummary{}, fmt.Errorf("re-embed %q: embedder returned %d vectors for %d chunks", d.SourceURI, len(vecs), len(chunks))
			}
			vectors = vecs
		} else {
			for i, c := range d.Chunks {
				vectors[i] = c.Vector
			}
		}
		entries := make([]VectorEntry, len(chunks))
		for i, c := range chunks {
			entries[i] = VectorEntry{ChunkID: c.ID, Vector: vectors[i], Metadata: meta}
		}
		if err := im.docs.Upsert(ctx, doc, chunks); err != nil {
			return TransferSummary{}, fmt.Errorf("import document %q: %w", d.SourceURI, err)
		}
		if err := im.index.Upsert(ctx, target, entries); err != nil {
			return TransferSummary{}, fmt.Errorf("import vectors for %q: %w", d.SourceURI, err)
		}
		if im.lexical != nil {
			if err := im.lexical.Upsert(ctx, target, lexicalDocs(chunks, meta)); err != nil {
				return TransferSummary{}, fmt.Errorf("import lexical for %q: %w", d.SourceURI, err)
			}
		}
		chunkCount += len(chunks)
	}

	return TransferSummary{
		Collection: target,
		Model:      space.Model,
		Dimensions: space.Dimensions,
		Documents:  len(b.Documents),
		Chunks:     chunkCount,
	}, nil
}
