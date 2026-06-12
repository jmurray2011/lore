package extract_test

import (
	"fmt"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/extract"
)

// fakeExtractor supports exactly one content type and returns a fixed text.
type fakeExtractor struct {
	ct   string
	text string
}

func (f fakeExtractor) Supports(ct string) bool { return ct == f.ct }

func (f fakeExtractor) Extract(ct string, _ []byte) (string, error) {
	if ct != f.ct {
		return "", fmt.Errorf("fake: unsupported %q", ct)
	}
	return f.text, nil
}

func TestRouter(t *testing.T) {
	r := extract.NewRouter(
		fakeExtractor{ct: "text/plain", text: "T"},
		fakeExtractor{ct: "application/pdf", text: "P"},
	)

	t.Run("Supports is the union of its extractors", func(t *testing.T) {
		for _, ct := range []string{"text/plain", "application/pdf"} {
			if !r.Supports(ct) {
				t.Errorf("want Supports(%q) = true", ct)
			}
		}
		if r.Supports("image/png") {
			t.Error("want Supports(image/png) = false")
		}
	})

	t.Run("Extract dispatches by content type", func(t *testing.T) {
		got, err := r.Extract("text/plain", nil)
		if err != nil || got != "T" {
			t.Errorf("text: got %q, %v", got, err)
		}
		got, err = r.Extract("application/pdf", nil)
		if err != nil || got != "P" {
			t.Errorf("pdf: got %q, %v", got, err)
		}
	})

	t.Run("unsupported content type is an error", func(t *testing.T) {
		if _, err := r.Extract("image/png", nil); err == nil {
			t.Error("want error for unsupported type")
		}
	})

	t.Run("first matching extractor wins", func(t *testing.T) {
		r := extract.NewRouter(
			fakeExtractor{ct: "text/plain", text: "first"},
			fakeExtractor{ct: "text/plain", text: "second"},
		)
		if got, _ := r.Extract("text/plain", nil); got != "first" {
			t.Errorf("got %q, want first", got)
		}
	})
}
