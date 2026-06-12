package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/openai"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestGeneratorSynthesize(t *testing.T) {
	ctx := context.Background()
	chunk, err := domain.NewChunk(domain.DeriveDocumentID("docs", "file:///a.md"), 0, "the sky is blue")
	if err != nil {
		t.Fatal(err)
	}
	hits := []domain.ChunkHit{{Chunk: chunk, Score: 0.9}}

	t.Run("builds a grounded request and returns an answer with citations", func(t *testing.T) {
		var gotPath string
		var gotReq struct {
			Model    string
			Messages []struct{ Role, Content string }
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":" The sky is blue. "}}]}`)
		}))
		defer srv.Close()

		g, err := openai.NewGenerator(srv.URL, "k", "gpt-test", srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		ans, err := g.Synthesize(ctx, "what color is the sky?", hits)
		if err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
		if ans.Text != "The sky is blue." {
			t.Errorf("answer text = %q", ans.Text)
		}
		if len(ans.Citations) != 1 || ans.Citations[0] != chunk.ID {
			t.Errorf("citations = %v, want [%s]", ans.Citations, chunk.ID)
		}
		if gotPath != "/chat/completions" {
			t.Errorf("path = %q", gotPath)
		}
		if gotReq.Model != "gpt-test" {
			t.Errorf("model = %q", gotReq.Model)
		}
		var user string
		for _, m := range gotReq.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		if !strings.Contains(user, "what color is the sky?") || !strings.Contains(user, "the sky is blue") {
			t.Errorf("user prompt missing question or context: %q", user)
		}
		if !strings.Contains(user, string(chunk.ID)) {
			t.Errorf("user prompt missing citation id %s: %q", chunk.ID, user)
		}
	})

	t.Run("no choices is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}))
		defer srv.Close()
		g, _ := openai.NewGenerator(srv.URL, "", "m", srv.Client())

		if _, err := g.Synthesize(ctx, "q", hits); err == nil {
			t.Error("want error when the API returns no choices")
		}
	})

	t.Run("non-2xx is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		g, _ := openai.NewGenerator(srv.URL, "", "m", srv.Client())

		if _, err := g.Synthesize(ctx, "q", hits); err == nil {
			t.Error("want error on HTTP 502")
		}
	})
}

func TestNewGeneratorValidation(t *testing.T) {
	if _, err := openai.NewGenerator("", "k", "m", nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("empty base url: want ErrInvalidArgument, got %v", err)
	}
	if _, err := openai.NewGenerator("http://x", "k", "", nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("empty model: want ErrInvalidArgument, got %v", err)
	}
}
