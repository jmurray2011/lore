package domain_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestHashContent(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := domain.HashContent([]byte("hello"))
		b := domain.HashContent([]byte("hello"))
		if a != b {
			t.Errorf("same content must hash equal: %s vs %s", a, b)
		}
	})

	t.Run("distinguishes content", func(t *testing.T) {
		if domain.HashContent([]byte("hello")) == domain.HashContent([]byte("hello!")) {
			t.Error("different content must hash differently")
		}
	})

	t.Run("hex sha256 shape", func(t *testing.T) {
		h := domain.HashContent(nil)
		if len(h) != 64 {
			t.Errorf("want 64 hex chars, got %d (%s)", len(h), h)
		}
	})
}
