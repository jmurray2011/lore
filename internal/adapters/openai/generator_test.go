package openai_test

import (
	"context"
	"encoding/base64"
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
			Model          string
			Messages       []struct{ Role, Content string }
			ResponseFormat *json.RawMessage `json:"response_format"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":" The sky is blue. "}}]}`)
		}))
		defer srv.Close()

		g, err := openai.NewGenerator(srv.URL, "k", "gpt-test", openai.Capabilities{}, openai.AuthBearer, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		ans, err := g.Synthesize(ctx, "what color is the sky?", hits, nil)
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
		if gotReq.ResponseFormat != nil {
			t.Errorf("plain-text mode must not send response_format, got %s", *gotReq.ResponseFormat)
		}
	})

	t.Run("structured mode requests json_schema and returns validated citations", func(t *testing.T) {
		var gotReq struct {
			ResponseFormat *struct {
				Type       string `json:"type"`
				JSONSchema *struct {
					Name   string         `json:"name"`
					Strict bool           `json:"strict"`
					Schema map[string]any `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			// The model returns one valid chunk ID and one bogus one.
			inner, _ := json.Marshal(map[string]any{
				"answer":    "Blue.",
				"citations": []string{string(chunk.ID), "no-such-chunk"},
			})
			resp, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": string(inner)}}},
			})
			_, _ = w.Write(resp)
		}))
		defer srv.Close()

		g, err := openai.NewGenerator(srv.URL, "k", "gpt-test", openai.Capabilities{StructuredOutput: true}, openai.AuthBearer, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		ans, err := g.Synthesize(ctx, "what color is the sky?", hits, nil)
		if err != nil {
			t.Fatalf("Synthesize: %v", err)
		}

		if gotReq.ResponseFormat == nil || gotReq.ResponseFormat.Type != "json_schema" {
			t.Fatalf("want response_format type json_schema, got %+v", gotReq.ResponseFormat)
		}
		js := gotReq.ResponseFormat.JSONSchema
		if js == nil || !js.Strict || js.Name == "" {
			t.Fatalf("want a named strict json_schema, got %+v", js)
		}
		if _, ok := js.Schema["properties"]; !ok {
			t.Errorf("schema missing properties: %+v", js.Schema)
		}
		if ans.Text != "Blue." {
			t.Errorf("answer text = %q", ans.Text)
		}
		// Bogus citation filtered out; only the real chunk ID survives.
		if len(ans.Citations) != 1 || ans.Citations[0] != chunk.ID {
			t.Errorf("citations = %v, want [%s]", ans.Citations, chunk.ID)
		}
	})

	t.Run("no choices is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}))
		defer srv.Close()
		g, _ := openai.NewGenerator(srv.URL, "", "m", openai.Capabilities{}, openai.AuthBearer, srv.Client())

		if _, err := g.Synthesize(ctx, "q", hits, nil); err == nil {
			t.Error("want error when the API returns no choices")
		}
	})

	t.Run("non-2xx is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		g, _ := openai.NewGenerator(srv.URL, "", "m", openai.Capabilities{}, openai.AuthBearer, srv.Client())

		if _, err := g.Synthesize(ctx, "q", hits, nil); err == nil {
			t.Error("want error on HTTP 502")
		}
	})
}

func TestGeneratorAttachments(t *testing.T) {
	ctx := context.Background()
	chunk, err := domain.NewChunk(domain.DeriveDocumentID("docs", "file:///a.md"), 0, "context")
	if err != nil {
		t.Fatal(err)
	}
	hits := []domain.ChunkHit{{Chunk: chunk, Score: 1}}
	img, err := domain.NewAttachment("image/png", "c.png", []byte{0x89, 0x50, 0x4e, 0x47})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := domain.NewAttachment("application/pdf", "d.pdf", []byte("%PDF-1.4 stub"))
	if err != nil {
		t.Fatal(err)
	}
	const answerBody = `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`

	t.Run("image encodes as image_url when capability is on", func(t *testing.T) {
		var body string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			_, _ = io.WriteString(w, answerBody)
		}))
		defer srv.Close()

		g, _ := openai.NewGenerator(srv.URL, "k", "m", openai.Capabilities{ImageInput: true}, openai.AuthBearer, srv.Client())
		if _, err := g.Synthesize(ctx, "q", hits, []domain.Attachment{img}); err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
		wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.Data)
		if !strings.Contains(body, `"type":"image_url"`) || !strings.Contains(body, wantURL) {
			t.Errorf("request missing image_url part: %s", body)
		}
	})

	t.Run("document encodes as a file part when capability is on", func(t *testing.T) {
		var body string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			_, _ = io.WriteString(w, answerBody)
		}))
		defer srv.Close()

		g, _ := openai.NewGenerator(srv.URL, "k", "m", openai.Capabilities{DocumentInput: true}, openai.AuthBearer, srv.Client())
		if _, err := g.Synthesize(ctx, "q", hits, []domain.Attachment{pdf}); err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
		wantURL := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf.Data)
		if !strings.Contains(body, `"type":"file"`) || !strings.Contains(body, wantURL) || !strings.Contains(body, `"filename":"d.pdf"`) {
			t.Errorf("request missing file part: %s", body)
		}
	})

	t.Run("image without capability errors before any request", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		defer srv.Close()

		g, _ := openai.NewGenerator(srv.URL, "k", "m", openai.Capabilities{}, openai.AuthBearer, srv.Client())
		if _, err := g.Synthesize(ctx, "q", hits, []domain.Attachment{img}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
		if called {
			t.Error("must not call the provider when the capability is off")
		}
	})

	t.Run("document without capability errors", func(t *testing.T) {
		g, _ := openai.NewGenerator("http://unused", "k", "m", openai.Capabilities{}, openai.AuthBearer, nil)
		if _, err := g.Synthesize(ctx, "q", hits, []domain.Attachment{pdf}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})
}

func TestNewGeneratorValidation(t *testing.T) {
	if _, err := openai.NewGenerator("", "k", "m", openai.Capabilities{}, openai.AuthBearer, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("empty base url: want ErrInvalidArgument, got %v", err)
	}
	if _, err := openai.NewGenerator("http://x", "k", "", openai.Capabilities{}, openai.AuthBearer, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("empty model: want ErrInvalidArgument, got %v", err)
	}
}
