package rerank_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/httpjson"
	"github.com/jmurray2011/lore/internal/adapters/rerank"
)

func TestReranker(t *testing.T) {
	ctx := context.Background()

	t.Run("parses a Cohere-style response and sends model/query/documents/top_n", func(t *testing.T) {
		var gotBody rerankReqJSON
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/rerank" {
				t.Errorf("path = %q, want /rerank", r.URL.Path)
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			_, _ = io.WriteString(w, `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.4}]}`)
		}))
		defer srv.Close()

		rr, err := rerank.NewReranker(srv.URL, "key", "rerank-model", httpjson.AuthBearer, srv.Client())
		if err != nil {
			t.Fatalf("NewReranker: %v", err)
		}
		got, err := rr.Rerank(ctx, "the query", []string{"alpha", "beta"}, 5)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(got) != 2 || got[0].Index != 1 || got[0].Score != 0.9 || got[1].Index != 0 {
			t.Errorf("parsed results wrong: %+v", got)
		}
		if gotBody.Model != "rerank-model" || gotBody.Query != "the query" || len(gotBody.Documents) != 2 || gotBody.TopN != 5 {
			t.Errorf("request body wrong: %+v", gotBody)
		}
	})

	t.Run("sends the configured auth header", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			auth   httpjson.Auth
			header string
			want   string
		}{
			{"bearer", httpjson.AuthBearer, "Authorization", "Bearer key"},
			{"api-key", httpjson.AuthAPIKey, "api-key", "key"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var got string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got = r.Header.Get(tc.header)
					_, _ = io.WriteString(w, `{"results":[]}`)
				}))
				defer srv.Close()
				rr, _ := rerank.NewReranker(srv.URL, "key", "m", tc.auth, srv.Client())
				if _, err := rr.Rerank(ctx, "q", []string{"d"}, 0); err != nil {
					t.Fatalf("Rerank: %v", err)
				}
				if got != tc.want {
					t.Errorf("%s = %q, want %q", tc.header, got, tc.want)
				}
			})
		}
	})

	t.Run("retries a 429 then succeeds", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.7}]}`)
		}))
		defer srv.Close()
		rr, _ := rerank.NewReranker(srv.URL, "", "m", httpjson.AuthBearer, srv.Client())
		got, err := rr.Rerank(ctx, "q", []string{"d"}, 0)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(got) != 1 || got[0].Score != 0.7 {
			t.Errorf("post-retry result wrong: %+v", got)
		}
		if calls.Load() != 2 {
			t.Errorf("want 2 calls (429 then 200), got %d", calls.Load())
		}
	})

	t.Run("a persistent 500 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		rr, _ := rerank.NewReranker(srv.URL, "", "m", httpjson.AuthBearer, srv.Client())
		if _, err := rr.Rerank(ctx, "q", []string{"d"}, 0); err == nil {
			t.Error("want an error on 500")
		}
	})

	t.Run("empty documents makes no request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("server must not be called for empty documents")
		}))
		defer srv.Close()
		rr, _ := rerank.NewReranker(srv.URL, "", "m", httpjson.AuthBearer, srv.Client())
		got, err := rr.Rerank(ctx, "q", nil, 0)
		if err != nil || len(got) != 0 {
			t.Errorf("want empty/nil, got %v / %v", got, err)
		}
	})

	t.Run("missing base URL or model is a usage error", func(t *testing.T) {
		if _, err := rerank.NewReranker("", "k", "m", httpjson.AuthBearer, nil); err == nil {
			t.Error("want an error for missing base URL")
		}
		if _, err := rerank.NewReranker("http://x", "k", "", httpjson.AuthBearer, nil); err == nil {
			t.Error("want an error for missing model")
		}
	})
}

type rerankReqJSON struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}
