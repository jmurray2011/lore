package domain_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestParseFrontMatter(t *testing.T) {
	t.Run("no leading fence returns text unchanged, no metadata", func(t *testing.T) {
		text := "# Heading\n\nbody text"
		md, body := domain.ParseFrontMatter(text)
		if md != nil {
			t.Errorf("want no metadata, got %v", md)
		}
		if body != text {
			t.Errorf("body must be unchanged, got %q", body)
		}
	})

	t.Run("parses scalar pairs and strips the block from the body", func(t *testing.T) {
		text := "---\nauthor: alice\ndate: 2025-06-01\n---\n# Title\n\nbody"
		md, body := domain.ParseFrontMatter(text)
		if md["author"] != "alice" || md["date"] != "2025-06-01" {
			t.Errorf("metadata mismatch: %v", md)
		}
		if body != "# Title\n\nbody" {
			t.Errorf("body not stripped: %q", body)
		}
	})

	t.Run("strips matching quotes from values", func(t *testing.T) {
		text := "---\ntitle: \"Q3 Incident Report\"\nteam: 'platform'\n---\nbody"
		md, _ := domain.ParseFrontMatter(text)
		if md["title"] != "Q3 Incident Report" || md["team"] != "platform" {
			t.Errorf("quotes not stripped: %v", md)
		}
	})

	t.Run("flattens a bracket list to a comma-joined value", func(t *testing.T) {
		text := "---\ntags: [security, compliance, audit]\n---\nbody"
		md, _ := domain.ParseFrontMatter(text)
		if md["tags"] != "security,compliance,audit" {
			t.Errorf("list not flattened: %q", md["tags"])
		}
	})

	t.Run("ignores blank lines, comments, and keyless lines", func(t *testing.T) {
		text := "---\n# a comment\nauthor: alice\n\nnotakey\n---\nbody"
		md, _ := domain.ParseFrontMatter(text)
		if len(md) != 1 || md["author"] != "alice" {
			t.Errorf("want only author=alice, got %v", md)
		}
	})

	t.Run("an unterminated block is not front matter", func(t *testing.T) {
		text := "---\nauthor: alice\nbody with no closing fence"
		md, body := domain.ParseFrontMatter(text)
		if md != nil || body != text {
			t.Errorf("unterminated block must be left intact: md=%v body=%q", md, body)
		}
	})

	t.Run("handles CRLF line endings", func(t *testing.T) {
		text := "---\r\nauthor: alice\r\n---\r\nbody"
		md, body := domain.ParseFrontMatter(text)
		if md["author"] != "alice" {
			t.Errorf("CRLF not handled: %v", md)
		}
		if body != "body" {
			t.Errorf("CRLF body not stripped: %q", body)
		}
	})

	t.Run("an empty block strips cleanly with no metadata", func(t *testing.T) {
		text := "---\n---\nbody"
		md, body := domain.ParseFrontMatter(text)
		if md != nil {
			t.Errorf("want no metadata, got %v", md)
		}
		if body != "body" {
			t.Errorf("body not stripped: %q", body)
		}
	})
}
