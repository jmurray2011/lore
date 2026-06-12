package domain_test

import (
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestNewEmbeddingSpace(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		s, err := domain.NewEmbeddingSpace("text-embedding-3-small", 1536)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.Model != "text-embedding-3-small" || s.Dimensions != 1536 {
			t.Errorf("got %+v", s)
		}
	})

	t.Run("rejects empty model", func(t *testing.T) {
		_, err := domain.NewEmbeddingSpace("", 1536)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("rejects non-positive dimensions", func(t *testing.T) {
		for _, dims := range []int{0, -1} {
			if _, err := domain.NewEmbeddingSpace("m", dims); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("dims=%d: want ErrInvalidArgument, got %v", dims, err)
			}
		}
	})
}

func TestEmbeddingSpaceEqual(t *testing.T) {
	a := domain.EmbeddingSpace{Model: "m", Dimensions: 8}
	b := domain.EmbeddingSpace{Model: "m", Dimensions: 8}
	c := domain.EmbeddingSpace{Model: "m", Dimensions: 16}
	d := domain.EmbeddingSpace{Model: "other", Dimensions: 8}

	if !a.Equal(b) {
		t.Error("identical spaces must be equal")
	}
	if a.Equal(c) {
		t.Error("different dimensions must not be equal")
	}
	if a.Equal(d) {
		t.Error("different models must not be equal")
	}
}

func TestEmbeddingSpaceIsZero(t *testing.T) {
	if !(domain.EmbeddingSpace{}).IsZero() {
		t.Error("zero value must report IsZero")
	}
	if (domain.EmbeddingSpace{Model: "m", Dimensions: 1}).IsZero() {
		t.Error("initialized space must not report IsZero")
	}
}
