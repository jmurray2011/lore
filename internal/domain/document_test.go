package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestNewDocument(t *testing.T) {
	now := time.Now()
	hash := domain.HashContent([]byte("content"))

	t.Run("valid", func(t *testing.T) {
		d, err := domain.NewDocument("docs", "file:///a.md", hash, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.ID == "" || d.Collection != "docs" || d.SourceURI != "file:///a.md" || d.Hash != hash {
			t.Errorf("got %+v", d)
		}
	})

	t.Run("rejects missing fields", func(t *testing.T) {
		cases := []struct {
			collection, uri string
			hash            domain.ContentHash
		}{
			{"", "file:///a.md", hash},
			{"docs", "", hash},
			{"docs", "file:///a.md", ""},
		}
		for _, c := range cases {
			if _, err := domain.NewDocument(c.collection, c.uri, c.hash, now); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("%+v: want ErrInvalidArgument, got %v", c, err)
			}
		}
	})
}

func TestDeriveDocumentID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		first := domain.DeriveDocumentID("docs", "file:///a.md")
		second := domain.DeriveDocumentID("docs", "file:///a.md")
		if first != second {
			t.Error("same (collection, source) must derive same ID")
		}
	})

	t.Run("scoped by collection", func(t *testing.T) {
		if domain.DeriveDocumentID("docs", "file:///a.md") == domain.DeriveDocumentID("notes", "file:///a.md") {
			t.Error("same source in different collections must derive different IDs")
		}
	})

	t.Run("no concatenation ambiguity", func(t *testing.T) {
		if domain.DeriveDocumentID("ab", "c") == domain.DeriveDocumentID("a", "bc") {
			t.Error("(ab,c) and (a,bc) must derive different IDs")
		}
	})
}

func TestDocumentUnchanged(t *testing.T) {
	hash := domain.HashContent([]byte("v1"))
	d, err := domain.NewDocument("docs", "file:///a.md", hash, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !d.Unchanged(domain.HashContent([]byte("v1"))) {
		t.Error("identical content must report Unchanged")
	}
	if d.Unchanged(domain.HashContent([]byte("v2"))) {
		t.Error("modified content must not report Unchanged")
	}
}
