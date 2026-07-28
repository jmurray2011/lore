package domain

import (
	"sort"
	"strings"
	"time"
)

// CorpusSnapshot is a content-derived identity for a collection's document set.
// Its Digest changes if and only if the set of (source, content-hash) pairs
// changes — any addition, removal, or edit — and is therefore the field a
// provenance trail should stamp, unlike Collection.CreatedAt, which is a birth/
// rebuild marker that a sync leaves untouched. LastIngest names when the most
// recently (re)ingested document entered the corpus.
//
// The snapshot is space- and chunker-independent by design: it identifies what
// was ingested, not how it was embedded, mirroring CollectionDiff.
type CorpusSnapshot struct {
	// Digest is the hex sha256 over the corpus's (SourceURI, Hash) pairs.
	Digest ContentHash
	// LastIngest is the maximum Document.IngestedAt, or the zero time when the
	// corpus is empty.
	LastIngest time.Time
}

// SnapshotOf computes the CorpusSnapshot of docs. The Digest is order-
// independent (the pairs are hashed in SourceURI order) and unambiguous: each
// field is NUL-terminated, so neither a URI nor a hash can straddle a boundary
// — the same framing guarantee DeriveDocumentID relies on.
func SnapshotOf(docs []*Document) CorpusSnapshot {
	sorted := make([]*Document, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SourceURI < sorted[j].SourceURI })

	var canonical strings.Builder
	var last time.Time
	for _, d := range sorted {
		canonical.WriteString(d.SourceURI)
		canonical.WriteByte(0)
		canonical.WriteString(string(d.Hash))
		canonical.WriteByte(0)
		if d.IngestedAt.After(last) {
			last = d.IngestedAt
		}
	}
	return CorpusSnapshot{
		Digest:     HashContent([]byte(canonical.String())),
		LastIngest: last,
	}
}
