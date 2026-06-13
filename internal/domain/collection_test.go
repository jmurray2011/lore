package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

var testSpace = domain.EmbeddingSpace{Model: "test-model", Dimensions: 4}

var testSpec = domain.ChunkerSpec{
	Strategy: "fixed", Version: 1, Size: 512, Overlap: 76, Tokenizer: "words",
}

func TestNewCollection(t *testing.T) {
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)

	t.Run("valid names", func(t *testing.T) {
		for _, name := range []string{"docs", "my-project.v2", "a", "0x", strings.Repeat("a", 64)} {
			c, err := domain.NewCollection(name, testSpace, testSpec, now)
			if err != nil {
				t.Errorf("name %q: unexpected error: %v", name, err)
				continue
			}
			if c.Name != name || !c.Space.Equal(testSpace) || c.Chunker != testSpec || !c.CreatedAt.Equal(now) {
				t.Errorf("name %q: got %+v", name, c)
			}
		}
	})

	t.Run("invalid names", func(t *testing.T) {
		for _, name := range []string{
			"",                      // empty
			"Docs",                  // uppercase
			"-leading",              // bad first char
			".hidden",               // bad first char
			"has space",             // whitespace
			"sl/ash",                // path separator
			strings.Repeat("a", 65), // too long
		} {
			if _, err := domain.NewCollection(name, testSpace, testSpec, now); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("name %q: want ErrInvalidArgument, got %v", name, err)
			}
		}
	})

	t.Run("rejects zero space", func(t *testing.T) {
		if _, err := domain.NewCollection("docs", domain.EmbeddingSpace{}, testSpec, now); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("rejects zero/invalid chunker spec", func(t *testing.T) {
		for _, spec := range []domain.ChunkerSpec{
			{},                  // zero
			{Strategy: "fixed"}, // missing version/size/tokenizer
			{Strategy: "fixed", Version: 1, Size: 0, Tokenizer: "words"},             // non-positive size
			{Strategy: "fixed", Version: 1, Size: 4, Overlap: 4, Tokenizer: "words"}, // overlap == size
		} {
			if _, err := domain.NewCollection("docs", testSpace, spec, now); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("spec %+v: want ErrInvalidArgument, got %v", spec, err)
			}
		}
	})
}

func TestCollectionAcceptsSpace(t *testing.T) {
	c, err := domain.NewCollection("docs", testSpace, testSpec, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("same space accepted", func(t *testing.T) {
		if err := c.AcceptsSpace(testSpace); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("different model rejected", func(t *testing.T) {
		err := c.AcceptsSpace(domain.EmbeddingSpace{Model: "other", Dimensions: 4})
		if !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Errorf("want ErrSpaceMismatch, got %v", err)
		}
	})

	t.Run("different dimensions rejected", func(t *testing.T) {
		err := c.AcceptsSpace(domain.EmbeddingSpace{Model: "test-model", Dimensions: 8})
		if !errors.Is(err, domain.ErrSpaceMismatch) {
			t.Errorf("want ErrSpaceMismatch, got %v", err)
		}
	})
}

func TestCollectionAcceptsChunker(t *testing.T) {
	c, err := domain.NewCollection("docs", testSpace, testSpec, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("identical spec accepted", func(t *testing.T) {
		if err := c.AcceptsChunker(testSpec); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("any differing field rejected", func(t *testing.T) {
		variants := []domain.ChunkerSpec{
			{Strategy: "structure", Version: 1, Size: 512, Overlap: 76, Tokenizer: "words"},
			{Strategy: "fixed", Version: 2, Size: 512, Overlap: 76, Tokenizer: "words"},
			{Strategy: "fixed", Version: 1, Size: 256, Overlap: 76, Tokenizer: "words"},
			{Strategy: "fixed", Version: 1, Size: 512, Overlap: 64, Tokenizer: "words"},
			{Strategy: "fixed", Version: 1, Size: 512, Overlap: 76, Tokenizer: "o200k_base"},
			{Strategy: "fixed", Version: 1, Size: 512, Overlap: 76, Tokenizer: "words", ContextPrefix: true},
		}
		for _, v := range variants {
			if err := c.AcceptsChunker(v); !errors.Is(err, domain.ErrChunkerMismatch) {
				t.Errorf("spec %+v: want ErrChunkerMismatch, got %v", v, err)
			}
		}
	})

	t.Run("legacy unpinned collection refuses with an explanatory error", func(t *testing.T) {
		legacy := &domain.Collection{Name: "old", Space: testSpace} // zero Chunker, as loaded from a pre-pin DB
		err := legacy.AcceptsChunker(testSpec)
		if !errors.Is(err, domain.ErrChunkerMismatch) {
			t.Fatalf("want ErrChunkerMismatch, got %v", err)
		}
		if !strings.Contains(err.Error(), "predates") {
			t.Errorf("legacy error should explain the collection predates pinning, got %q", err)
		}
	})
}
