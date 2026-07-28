package domain

import (
	"fmt"
	"regexp"
	"time"
)

// collectionNameRE: non-empty, filesystem- and shell-safe, max 64 chars.
var collectionNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateCollectionName reports whether name is non-empty,
// filesystem- and shell-safe, and at most 64 chars. It is the shared rule behind
// NewCollection and the import path, which reconstructs a (possibly renamed)
// collection from an artifact and must validate the target name without building
// a full collection.
func ValidateCollectionName(name string) error {
	if !collectionNameRE.MatchString(name) {
		return fmt.Errorf("collection name %q: %w: must match %s", name, ErrInvalidArgument, collectionNameRE)
	}
	return nil
}

// Collection is the aggregate root: a named corpus bound to exactly one
// EmbeddingSpace, and pinned to exactly one ChunkerSpec, for its entire lifetime.
type Collection struct {
	Name      string
	Space     EmbeddingSpace
	Chunker   ChunkerSpec
	CreatedAt time.Time
	// Sources are the paths ingested into this collection, recorded by add and
	// sync so `lore sync` can replay them without a path argument.
	Sources []string
}

// NewCollection validates identity, space, and chunker spec, and constructs a
// Collection. The clock is injected so the domain stays deterministic and
// testable. A new collection must be pinned to a valid ChunkerSpec; collections
// loaded from storage that predate chunker pinning carry a zero spec (built as a
// literal by the repository, not through this constructor).
func NewCollection(name string, space EmbeddingSpace, chunker ChunkerSpec, now time.Time) (*Collection, error) {
	if err := ValidateCollectionName(name); err != nil {
		return nil, err
	}
	if space.IsZero() {
		return nil, fmt.Errorf("collection %q: %w: embedding space is required", name, ErrInvalidArgument)
	}
	if err := chunker.Validate(); err != nil {
		return nil, fmt.Errorf("collection %q: %w", name, err)
	}
	return &Collection{Name: name, Space: space, Chunker: chunker, CreatedAt: now}, nil
}

// SameSpace enforces space coherence across a set of collections: their vectors are
// directly comparable only if every collection shares one EmbeddingSpace. It is
// the precondition for any cross-collection retrieval — feeding one collection's
// vectors into another (query --from-collection) or merging hits from several
// (multi-collection ask/query) — and returns ErrSpaceMismatch naming the first
// offending collection (and both spaces) on a divergence. Zero or one collection
// trivially shares a space.
func SameSpace(colls []*Collection) error {
	if len(colls) < 2 {
		return nil
	}
	base := colls[0]
	for _, c := range colls[1:] {
		if !c.Space.Equal(base.Space) {
			return fmt.Errorf("collections %q (%s) and %q (%s) are in different embedding spaces; their vectors are not comparable: %w",
				base.Name, base.Space, c.Name, c.Space, ErrSpaceMismatch)
		}
	}
	return nil
}

// AcceptsSpace enforces space coherence: vectors may enter the
// collection only if they were produced in the collection's own space.
func (c *Collection) AcceptsSpace(s EmbeddingSpace) error {
	if !c.Space.Equal(s) {
		return fmt.Errorf("collection %q is bound to embedding space %s, but the active embedder is %s; configure an embedder that serves %s, or rebuild the collection under the new model: %w", c.Name, c.Space, s, c.Space, ErrSpaceMismatch)
	}
	return nil
}

// AcceptsChunker enforces chunker coherence: a collection may be (re-)ingested
// only by the chunker it was pinned to. Mixing chunker configurations within a
// collection would silently leave it holding chunks of two incompatible layouts
// (unchanged documents fast-skip re-chunking), so a mismatch is refused — the
// collection must be rebuilt under the new chunker. A collection that predates
// chunker pinning (zero spec) is read-only: queryable, but not re-ingestable.
func (c *Collection) AcceptsChunker(spec ChunkerSpec) error {
	if c.Chunker == spec {
		return nil
	}
	if c.Chunker.IsZero() {
		return fmt.Errorf("collection %q predates chunker pinning (created by an older lore); rebuild it to ingest with the %s chunker: %w", c.Name, spec, ErrChunkerMismatch)
	}
	return fmt.Errorf("collection %q is pinned to chunker %s, active chunker is %s; rebuild the collection to change chunkers: %w", c.Name, c.Chunker, spec, ErrChunkerMismatch)
}
