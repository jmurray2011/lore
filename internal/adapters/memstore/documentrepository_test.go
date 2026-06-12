package memstore_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/conformance"
)

func TestDocumentRepositoryConformance(t *testing.T) {
	conformance.RunDocumentRepositorySuite(t, func(t *testing.T) app.DocumentRepository {
		return memstore.NewDocumentRepository()
	})
}
