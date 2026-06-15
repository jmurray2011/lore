package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// docFixtures builds documents from a set of source URIs for resolver tests.
func docFixtures(t *testing.T, uris ...string) []*domain.Document {
	t.Helper()
	docs := make([]*domain.Document, len(uris))
	for i, uri := range uris {
		d, err := domain.NewDocument("c", uri, domain.HashContent([]byte(uri)), time.Unix(0, 0))
		if err != nil {
			t.Fatalf("NewDocument(%q): %v", uri, err)
		}
		docs[i] = d
	}
	return docs
}

func TestResolveDocURI(t *testing.T) {
	t.Parallel()
	docs := docFixtures(t,
		"file:///corpus/ssp-v1.md",
		"file:///corpus/ssp-v2.md",
		"file:///corpus/notes/readme.md",
		"file:///corpus/Tenant2 (1).pdf",
	)

	tests := []struct {
		name     string
		selector string
		want     string
	}{
		{"exact full URI is unchanged (backward compatible)", "file:///corpus/ssp-v1.md", "file:///corpus/ssp-v1.md"},
		{"exact basename resolves to the full URI", "readme.md", "file:///corpus/notes/readme.md"},
		{"glob on basename resolves a single match", "Tenant2*", "file:///corpus/Tenant2 (1).pdf"},
		{"substring resolves a single match", "Tenant2 (1)", "file:///corpus/Tenant2 (1).pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveDocURI(docs, tt.selector)
			if err != nil {
				t.Fatalf("resolveDocURI(%q): %v", tt.selector, err)
			}
			if got != tt.want {
				t.Errorf("resolveDocURI(%q) = %q, want %q", tt.selector, got, tt.want)
			}
		})
	}
}

func TestResolveDocURINoMatch(t *testing.T) {
	t.Parallel()
	docs := docFixtures(t, "file:///corpus/ssp-v1.md")
	_, err := resolveDocURI(docs, "nonesuch.md")
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestResolveDocURIAmbiguous(t *testing.T) {
	t.Parallel()
	docs := docFixtures(t,
		"file:///corpus/ssp-v1.md",
		"file:///corpus/ssp-v2.md",
	)
	// "ssp" is a substring of two distinct documents: the selector must be refined.
	_, err := resolveDocURI(docs, "ssp")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument (ambiguous), got %v", err)
	}
}

func TestResolveDocURIEmptySelector(t *testing.T) {
	t.Parallel()
	docs := docFixtures(t, "file:///corpus/ssp-v1.md")
	if _, err := resolveDocURI(docs, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument for empty selector, got %v", err)
	}
}

// A more specific tier wins over a broader one: an exact basename match must not
// be drowned out by other documents that share a substring.
func TestResolveDocURITierPrecedence(t *testing.T) {
	t.Parallel()
	docs := docFixtures(t,
		"file:///a/report.md",
		"file:///b/report-draft.md",
	)
	got, err := resolveDocURI(docs, "report.md")
	if err != nil {
		t.Fatalf("resolveDocURI: %v", err)
	}
	if got != "file:///a/report.md" {
		t.Errorf("exact basename should win; got %q", got)
	}
}
