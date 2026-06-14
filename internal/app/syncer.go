package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/jmurray2011/lore/internal/domain"
)

// SyncSummary reports the outcome of a sync run.
type SyncSummary struct {
	Added       int      // documents ingested (new or changed)
	Skipped     int      // unchanged or empty documents (idempotent no-ops)
	Unsupported int      // documents whose content type no extractor handles
	Chunks      int      // chunks embedded and stored
	Pruned      int      // documents removed (or, in a dry run, that would be)
	PrunedURIs  []string // source URIs of the pruned documents, sorted
}

// Syncer brings a collection into line with its source(s). It re-ingests
// (add/update, idempotently) and, when prune is set, removes
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
//
// dryRun previews the prune set and changes nothing — no ingestion, no removal;
// it only applies to --prune (ErrInvalidArgument otherwise). The prune set does
// not depend on ingesting first: a freshly added document is always present at
// its source, so it is never in the removal set.
func (s *Syncer) Sync(ctx context.Context, collection string, paths []string, prune, dryRun bool) (SyncSummary, error) {
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

	if dryRun {
		if !prune {
			return SyncSummary{}, fmt.Errorf("sync %q: %w: --dry-run only previews --prune; pass --prune too", collection, domain.ErrInvalidArgument)
		}
		uris, err := s.prunable(ctx, collection, paths)
		if err != nil {
			return SyncSummary{}, err
		}
		return SyncSummary{Pruned: len(uris), PrunedURIs: uris}, nil
	}

	var sum SyncSummary
	for _, p := range paths {
		is, err := s.ingestor.Ingest(ctx, collection, p)
		if err != nil {
			return SyncSummary{}, err
		}
		sum.Added += is.Added
		sum.Skipped += is.Skipped
		sum.Unsupported += is.Unsupported
		sum.Chunks += is.Chunks
	}

	if prune {
		uris, err := s.prunable(ctx, collection, paths)
		if err != nil {
			return SyncSummary{}, err
		}
		for _, uri := range uris {
			if err := s.remover.RemoveDocument(ctx, collection, uri); err != nil {
				return SyncSummary{}, fmt.Errorf("prune %q: %w", uri, err)
			}
		}
		sum.Pruned = len(uris)
		sum.PrunedURIs = uris
	}
	return sum, nil
}

// prunable returns the source URIs of documents in the collection that are no
// longer present under any of the given paths, sorted for stable output. It
// walks the source only for URIs (no content), then diffs against the stored
// documents; the caller decides whether to actually remove them.
func (s *Syncer) prunable(ctx context.Context, collection string, paths []string) ([]string, error) {
	present := make(map[string]bool)
	for _, p := range paths {
		if err := s.source.Walk(ctx, p, func(it SourceItem) error {
			present[it.URI] = true
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk %q: %w", p, err)
		}
	}

	docs, err := s.catalog.ListDocuments(ctx, collection)
	if err != nil {
		return nil, err
	}
	var absent []string
	for _, d := range docs {
		if !present[d.SourceURI] {
			absent = append(absent, d.SourceURI)
		}
	}
	sort.Strings(absent)
	return absent, nil
}
