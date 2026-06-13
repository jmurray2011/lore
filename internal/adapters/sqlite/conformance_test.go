package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/sqlite"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/conformance"
)

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "lore.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCollectionRepositoryConformance(t *testing.T) {
	conformance.RunCollectionRepositorySuite(t, func(t *testing.T) app.CollectionRepository {
		return newStore(t).Collections()
	})
}

func TestDocumentRepositoryConformance(t *testing.T) {
	conformance.RunDocumentRepositorySuite(t, func(t *testing.T) app.DocumentRepository {
		return newStore(t).Documents()
	})
}

func TestVectorIndexConformance(t *testing.T) {
	conformance.RunVectorIndexSuite(t, func(t *testing.T) app.VectorIndex {
		return newStore(t).Vectors()
	})
}

func TestAnswerCacheConformance(t *testing.T) {
	conformance.RunAnswerCacheSuite(t, func(t *testing.T) app.AnswerCache {
		return newStore(t).Cache()
	})
}
