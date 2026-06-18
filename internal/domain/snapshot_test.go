package domain_test

import (
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

func doc(uri string, hash domain.ContentHash, ingested time.Time) *domain.Document {
	return &domain.Document{SourceURI: uri, Hash: hash, IngestedAt: ingested}
}

func TestSnapshotOf_DigestOrderIndependent(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	a := []*domain.Document{
		doc("file:///a.md", "h1", ts),
		doc("file:///b.md", "h2", ts),
		doc("file:///c.md", "h3", ts),
	}
	b := []*domain.Document{
		doc("file:///c.md", "h3", ts),
		doc("file:///a.md", "h1", ts),
		doc("file:///b.md", "h2", ts),
	}
	if got, want := domain.SnapshotOf(a).Digest, domain.SnapshotOf(b).Digest; got != want {
		t.Fatalf("digest depends on order: %q != %q", got, want)
	}
}

func TestSnapshotOf_DigestSensitiveToContent(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	base := []*domain.Document{
		doc("file:///a.md", "h1", ts),
		doc("file:///b.md", "h2", ts),
	}
	baseDigest := domain.SnapshotOf(base).Digest

	edited := []*domain.Document{
		doc("file:///a.md", "h1-changed", ts),
		doc("file:///b.md", "h2", ts),
	}
	added := []*domain.Document{
		doc("file:///a.md", "h1", ts),
		doc("file:///b.md", "h2", ts),
		doc("file:///c.md", "h3", ts),
	}
	removed := []*domain.Document{
		doc("file:///a.md", "h1", ts),
	}
	for name, docs := range map[string][]*domain.Document{
		"edit":   edited,
		"add":    added,
		"remove": removed,
	} {
		if domain.SnapshotOf(docs).Digest == baseDigest {
			t.Errorf("%s did not change the digest", name)
		}
	}
}

// A digest must not collide when a NUL-free byte boundary could otherwise be
// ambiguous between (a,bc) and (ab,c) — the same framing guarantee as
// DeriveDocumentID.
func TestSnapshotOf_DigestFramingNoCollision(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	x := []*domain.Document{doc("a", "bc", ts)}
	y := []*domain.Document{doc("ab", "c", ts)}
	if domain.SnapshotOf(x).Digest == domain.SnapshotOf(y).Digest {
		t.Fatal("(a,bc) and (ab,c) produced the same digest")
	}
}

func TestSnapshotOf_LastIngestIsMax(t *testing.T) {
	t.Parallel()
	older := time.Date(2026, 6, 14, 7, 3, 16, 0, time.UTC)
	newer := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	docs := []*domain.Document{
		doc("file:///a.md", "h1", newer),
		doc("file:///b.md", "h2", older),
	}
	if got := domain.SnapshotOf(docs).LastIngest; !got.Equal(newer) {
		t.Fatalf("LastIngest = %s, want %s (the max)", got, newer)
	}
}

func TestSnapshotOf_Empty(t *testing.T) {
	t.Parallel()
	got := domain.SnapshotOf(nil)
	if !got.LastIngest.IsZero() {
		t.Errorf("empty corpus LastIngest = %s, want zero", got.LastIngest)
	}
	if got.Digest == "" {
		t.Error("empty corpus should still have a stable digest, got empty string")
	}
	if got.Digest != domain.SnapshotOf([]*domain.Document{}).Digest {
		t.Error("nil and empty-slice corpora must share a digest")
	}
}
