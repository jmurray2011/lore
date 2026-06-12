package app

import (
	"context"
	"fmt"

	"github.com/jmurray2011/lore/internal/domain"
)

// SyncSummary reports the outcome of a sync run.
type SyncSummary struct {
	Added   int // documents ingested (new or changed)
	Skipped int // unchanged, unsupported, or empty documents
	Chunks  int // chunks embedded and stored
	Pruned  int // documents removed because their source no longer exists
}

// Syncer brings a collection into line with its source(s). It re-ingests
// (add/update, idempotently — invariant 2) and, when prune is set, removes
// documents whose source files no longer exist. With no paths it replays the
// sources the collection remembers from prior add/sync runs.
type Syncer struct {
	catalog  *Catalog
	ingestor *Ingestor
	remover  *Remover
	source   Source
}

// NewSyncer wires a Syncer over the use cases and the Source it walks for prune.
func NewSyncer(catalog *Catalog, ingestor *Ingestor, remover *Remover, source Source) *Syncer {
	return &Syncer{catalog: catalog, ingestor: ingestor, remover: remover, source: source}
}

// Sync ingests paths into the collection and, when prune is set, removes
// documents no longer present at the source. With no paths, the collection's
// remembered sources are replayed; with none recorded it fails with
// ErrInvalidArgument. An unknown collection is ErrNotFound.
func (s *Syncer) Sync(ctx context.Context, collection string, paths []string, prune bool) (SyncSummary, error) {
	coll, err := s.catalog.Get(ctx, collection)
	if err != nil {
		return SyncSummary{}, err
	}
	if len(paths) == 0 {
		paths = coll.Sources
	}
	if len(paths) == 0 {
		return SyncSummary{}, fmt.Errorf("sync %q: %w: no path given and no source remembered; pass a path once", collection, domain.ErrInvalidArgument)
	}

	var sum SyncSummary
	for _, p := range paths {
		is, err := s.ingestor.Ingest(ctx, collection, p)
		if err != nil {
			return SyncSummary{}, err
		}
		sum.Added += is.Added
		sum.Skipped += is.Skipped
		sum.Chunks += is.Chunks
	}

	if prune {
		pruned, err := s.prune(ctx, collection, paths)
		if err != nil {
			return SyncSummary{}, err
		}
		sum.Pruned = pruned
	}
	return sum, nil
}

// prune removes every document in the collection whose source URI is not present
// under any of the given paths. It walks the source only for URIs (no content),
// then diffs against the stored documents and deletes the absent ones (cascade
// via the Remover).
func (s *Syncer) prune(ctx context.Context, collection string, paths []string) (int, error) {
	present := make(map[string]bool)
	for _, p := range paths {
		if err := s.source.Walk(ctx, p, func(it SourceItem) error {
			present[it.URI] = true
			return nil
		}); err != nil {
			return 0, fmt.Errorf("walk %q: %w", p, err)
		}
	}

	docs, err := s.catalog.ListDocuments(ctx, collection)
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, d := range docs {
		if present[d.SourceURI] {
			continue
		}
		if err := s.remover.RemoveDocument(ctx, collection, d.SourceURI); err != nil {
			return 0, fmt.Errorf("prune %q: %w", d.SourceURI, err)
		}
		pruned++
	}
	return pruned, nil
}
