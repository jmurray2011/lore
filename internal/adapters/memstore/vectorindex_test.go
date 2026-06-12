package memstore_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/conformance"
)

func TestVectorIndexConformance(t *testing.T) {
	conformance.RunVectorIndexSuite(t, func(t *testing.T) app.VectorIndex {
		return memstore.NewVectorIndex()
	})
}
