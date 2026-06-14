package memstore_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/memstore"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/conformance"
)

func TestLexicalIndexConformance(t *testing.T) {
	conformance.RunLexicalIndexSuite(t, func(t *testing.T) app.LexicalIndex {
		return memstore.NewLexicalIndex()
	})
}
