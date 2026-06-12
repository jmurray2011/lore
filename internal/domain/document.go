package domain

import (
	"fmt"
	"time"
)

// DocumentID is deterministic, derived from (collection, source URI):
// re-ingesting the same source addresses the same identity, which is what
// makes `lore add` an upsert rather than an append.
type DocumentID string

// Document is source content with identity. Whether its content *changed*
// is decided by Hash (invariant 2: hash equality => ingestion is a no-op).
type Document struct {
	ID         DocumentID
	Collection string
	SourceURI  string
	Hash       ContentHash
	IngestedAt time.Time
	// Fingerprint is a cheap source-side signature (e.g. size + sampled-content
	// hash) used to skip re-reading unchanged files before extraction. Unlike
	// Hash it is a heuristic, not an identity; empty means "unknown".
	Fingerprint string
}

// NewDocument constructs a Document with its deterministic ID.
func NewDocument(collection, sourceURI string, hash ContentHash, now time.Time) (*Document, error) {
	if collection == "" || sourceURI == "" {
		return nil, fmt.Errorf("document: %w: collection and source URI must not be empty", ErrInvalidArgument)
	}
	if hash == "" {
		return nil, fmt.Errorf("document %q: %w: content hash is required", sourceURI, ErrInvalidArgument)
	}
	return &Document{
		ID:         DeriveDocumentID(collection, sourceURI),
		Collection: collection,
		SourceURI:  sourceURI,
		Hash:       hash,
		IngestedAt: now,
	}, nil
}

// DeriveDocumentID computes the deterministic identity of a source within a
// collection. The NUL separator prevents ambiguity between (a,bc) and (ab,c).
func DeriveDocumentID(collection, sourceURI string) DocumentID {
	return DocumentID(HashContent([]byte(collection + "\x00" + sourceURI)))
}

// Unchanged reports whether content with the given hash would be a no-op
// re-ingestion of this document.
func (d *Document) Unchanged(hash ContentHash) bool { return d.Hash == hash }
