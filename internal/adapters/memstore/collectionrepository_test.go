package memstore_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/conformance"
)

func TestCollectionRepositoryConformance(t *testing.T) {
	conformance.RunCollectionRepositorySuite(t, func(t *testing.T) app.CollectionRepository {
		return memstore.NewCollectionRepository()
	})
}
