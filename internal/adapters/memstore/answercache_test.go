package memstore_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/conformance"
)

func TestAnswerCacheConformance(t *testing.T) {
	conformance.RunAnswerCacheSuite(t, func(t *testing.T) app.AnswerCache {
		return memstore.NewAnswerCache()
	})
}
