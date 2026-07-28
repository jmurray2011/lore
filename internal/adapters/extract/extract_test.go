package extract_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/extract"
)

func TestExtractorSupports(t *testing.T) {
	e := extract.New()
	for _, ct := range []string{"text/plain", "text/markdown", "text/csv", "text/csv; charset=utf-8", "text/plain; charset=utf-8", "TEXT/MARKDOWN"} {
		if !e.Supports(ct) {
			t.Errorf("want Supports(%q) = true", ct)
		}
	}
	for _, ct := range []string{"application/octet-stream", "image/png", ""} {
		if e.Supports(ct) {
			t.Errorf("want Supports(%q) = false", ct)
		}
	}
}

func TestExtractorExtract(t *testing.T) {
	e := extract.New()

	t.Run("plain text normalizes CRLF", func(t *testing.T) {
		got, err := e.Extract("text/plain", []byte("a\r\nb\r\n"))
		if err != nil || got != "a\nb\n" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("markdown passes through", func(t *testing.T) {
		got, err := e.Extract("text/markdown", []byte("# H\n\ntext"))
		if err != nil || got != "# H\n\ntext" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("csv normalizes CRLF", func(t *testing.T) {
		got, err := e.Extract("text/csv", []byte("id,status\r\n1,open\r\n"))
		if err != nil || got != "id,status\n1,open\n" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("unsupported content type is an error", func(t *testing.T) {
		if _, err := e.Extract("image/png", []byte("x")); err == nil {
			t.Error("want error for unsupported type")
		}
	})

	t.Run("invalid UTF-8 is an error", func(t *testing.T) {
		if _, err := e.Extract("text/plain", []byte{0xff, 0xfe}); err == nil {
			t.Error("want error for invalid UTF-8")
		}
	})
}
