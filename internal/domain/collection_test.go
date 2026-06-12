package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

var testSpace = domain.EmbeddingSpace{Model: "test-model", Dimensions: 4}

func TestNewCollection(t *testing.T) {
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)

	t.Run("valid names", func(t *testing.T) {
		for _, name := range []string{"docs", "my-project.v2", "a", "0x", strings.Repeat("a", 64)} {
			c, err := domain.NewCollection(name, testSpace, now)
			if err != nil {
				t.Errorf("name %q: unexpected error: %v", name, err)
				continue
			}
			if c.Name != name || !c.Space.Equal(testSpace) || !c.CreatedAt.Equal(now) {
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
			if _, err := domain.NewCollection(name, testSpace, now); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("name %q: want ErrInvalidArgument, got %v", name, err)
			}
		}
	})

	t.Run("rejects zero space", func(t *testing.T) {
		if _, err := domain.NewCollection("docs", domain.EmbeddingSpace{}, now); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})
}

func TestCollectionAcceptsSpace(t *testing.T) {
	c, err := domain.NewCollection("docs", testSpace, time.Now())
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
