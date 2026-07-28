package domain_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestMetadataClone(t *testing.T) {
	t.Run("nil clones to nil", func(t *testing.T) {
		var md domain.Metadata
		if md.Clone() != nil {
			t.Error("cloning nil metadata should return nil")
		}
	})

	t.Run("clone is an independent copy", func(t *testing.T) {
		orig := domain.Metadata{"author": "alice", "tags": "security"}
		clone := orig.Clone()
		clone["author"] = "bob"
		clone["new"] = "value"
		if orig["author"] != "alice" {
			t.Error("mutating the clone must not affect the original")
		}
		if _, ok := orig["new"]; ok {
			t.Error("adding to the clone must not affect the original")
		}
	})
}
