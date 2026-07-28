package openai_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/openai"
)

func TestVerifierVerify(t *testing.T) {
	ctx := context.Background()

	t.Run("parses a JSON verdict from the model reply", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			// Reply wraps the JSON in prose to exercise lenient extraction.
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Sure: {\"supported\": true, \"rationale\": \"the evidence states it\"}"}}]}`)
		}))
		defer srv.Close()

		v, err := openai.NewVerifier(srv.URL, "k", "gpt-test", false, openai.AuthBearer, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.Verify(ctx, "the sky is blue", "the sky is blue today")
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if !got.Supported || got.Rationale != "the evidence states it" {
			t.Errorf("verdict = %+v", got)
		}
		if !strings.Contains(gotBody, "the sky is blue today") {
			t.Errorf("request should carry the evidence, got %q", gotBody)
		}
	})

	t.Run("an unsupported verdict is parsed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"supported\": false, \"rationale\": \"not stated\"}"}}]}`)
		}))
		defer srv.Close()
		v, _ := openai.NewVerifier(srv.URL, "k", "gpt-test", false, openai.AuthBearer, srv.Client())
		got, err := v.Verify(ctx, "claim", "evidence")
		if err != nil {
			t.Fatal(err)
		}
		if got.Supported {
			t.Errorf("want unsupported, got %+v", got)
		}
	})

	t.Run("empty evidence is unsupported without a model call", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"supported\":true}"}}]}`)
		}))
		defer srv.Close()
		v, _ := openai.NewVerifier(srv.URL, "k", "gpt-test", false, openai.AuthBearer, srv.Client())
		got, err := v.Verify(ctx, "claim", "   ")
		if err != nil {
			t.Fatal(err)
		}
		if got.Supported || called {
			t.Errorf("empty evidence should be unsupported with no call; supported=%v called=%v", got.Supported, called)
		}
	})

	t.Run("an unparseable reply is treated as unsupported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"I cannot decide."}}]}`)
		}))
		defer srv.Close()
		v, _ := openai.NewVerifier(srv.URL, "k", "gpt-test", false, openai.AuthBearer, srv.Client())
		got, err := v.Verify(ctx, "claim", "evidence")
		if err != nil {
			t.Fatal(err)
		}
		if got.Supported {
			t.Errorf("unparseable verdict must default to unsupported, got %+v", got)
		}
	})
}
