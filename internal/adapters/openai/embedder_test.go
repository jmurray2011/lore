package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/adapters/openai"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestEmbedderHonorsClientTimeout(t *testing.T) {
	// The handler hangs until the test ends, so the only way Embed returns is
	// the http.Client's timeout firing — the protection against a provider that
	// accepts a connection then never responds (decision 36).
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	// defers run LIFO: unblock the handler first, then Close (which waits for
	// the in-flight request to finish) can complete.
	defer srv.Close()
	defer close(release)

	hc := &http.Client{Timeout: 50 * time.Millisecond}
	e, err := openai.NewEmbedder(srv.URL, "k", "m", 2, openai.AuthBearer, hc)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.Embed(context.Background(), []string{"hi"}); err == nil {
		t.Fatal("expected a timeout error, got nil")
	} else {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Errorf("want a timeout error, got %v", err)
		}
	}
}

func TestClientRetriesOnRateLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("retries after 429 honoring Retry-After, then succeeds", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if atomic.AddInt32(&calls, 1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":"rate limited"}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
		}))
		defer srv.Close()

		e, _ := openai.NewEmbedder(srv.URL, "k", "m", 2, openai.AuthBearer, srv.Client())
		got, err := e.Embed(ctx, []string{"a"})
		if err != nil {
			t.Fatalf("Embed should succeed after retry: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d vectors, want 1", len(got))
		}
		if n := atomic.LoadInt32(&calls); n != 2 {
			t.Errorf("want 2 calls (429 then 200), got %d", n)
		}
	})

	t.Run("gives up after persistent 429 and returns an error", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		e, _ := openai.NewEmbedder(srv.URL, "k", "m", 2, openai.AuthBearer, srv.Client())
		if _, err := e.Embed(ctx, []string{"a"}); err == nil {
			t.Error("want error after exhausting retries")
		}
		if n := atomic.LoadInt32(&calls); n < 2 {
			t.Errorf("want multiple attempts, got %d", n)
		}
	})
}

func TestEmbedderSpace(t *testing.T) {
	e, err := openai.NewEmbedder("http://x", "k", "text-embedding-3-small", 1536, openai.AuthBearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := e.Space(context.Background())
	if err != nil {
		t.Fatalf("Space: %v", err)
	}
	if sp.Model != "text-embedding-3-small" || sp.Dimensions != 1536 {
		t.Errorf("space = %+v", sp)
	}
}

func TestEmbedderEmbed(t *testing.T) {
	ctx := context.Background()

	t.Run("returns vectors in input order, with auth and JSON body", func(t *testing.T) {
		var gotAuth, gotPath, gotCT string
		var gotReq struct {
			Model string
			Input []string
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			gotCT = r.Header.Get("Content-Type")
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			// Respond out of input order to exercise index-based reordering.
			_, _ = io.WriteString(w, `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`)
		}))
		defer srv.Close()

		e, err := openai.NewEmbedder(srv.URL, "secret", "m", 2, openai.AuthBearer, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.Embed(ctx, []string{"a", "b"})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(got) != 2 || got[0][0] != 1 || got[0][1] != 0 || got[1][0] != 0 || got[1][1] != 1 {
			t.Errorf("vectors not reordered by index: %v", got)
		}
		if gotAuth != "Bearer secret" {
			t.Errorf("Authorization = %q", gotAuth)
		}
		if gotPath != "/embeddings" {
			t.Errorf("path = %q", gotPath)
		}
		if gotCT != "application/json" {
			t.Errorf("Content-Type = %q", gotCT)
		}
		if gotReq.Model != "m" || len(gotReq.Input) != 2 {
			t.Errorf("request = %+v", gotReq)
		}
	})

	t.Run("api-key auth sends the api-key header, not Authorization", func(t *testing.T) {
		var gotAuth, gotAPIKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotAPIKey = r.Header.Get("api-key")
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
		}))
		defer srv.Close()

		e, err := openai.NewEmbedder(srv.URL, "secret", "m", 2, openai.AuthAPIKey, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Embed(ctx, []string{"a"}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if gotAPIKey != "secret" {
			t.Errorf("api-key header = %q, want secret", gotAPIKey)
		}
		if gotAuth != "" {
			t.Errorf("Authorization must be empty in api-key mode, got %q", gotAuth)
		}
	})

	t.Run("empty input makes no request", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		defer srv.Close()
		e, _ := openai.NewEmbedder(srv.URL, "", "m", 2, openai.AuthBearer, srv.Client())

		got, err := e.Embed(ctx, nil)
		if err != nil || len(got) != 0 {
			t.Errorf("got %v, %v; want empty, nil", got, err)
		}
		if called {
			t.Error("must not call the API for empty input")
		}
	})

	t.Run("dimension mismatch is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`)
		}))
		defer srv.Close()
		e, _ := openai.NewEmbedder(srv.URL, "", "m", 2, openai.AuthBearer, srv.Client()) // expects 2 dims, gets 3

		if _, err := e.Embed(ctx, []string{"a"}); err == nil {
			t.Error("want dimension-mismatch error")
		}
	})

	t.Run("non-2xx is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"boom"}`)
		}))
		defer srv.Close()
		e, _ := openai.NewEmbedder(srv.URL, "", "m", 2, openai.AuthBearer, srv.Client())

		if _, err := e.Embed(ctx, []string{"a"}); err == nil {
			t.Error("want error on HTTP 500")
		}
	})
}

func TestNewEmbedderValidation(t *testing.T) {
	cases := []struct {
		name, baseURL, model string
		dims                 int
	}{
		{"empty base url", "", "m", 2},
		{"empty model", "http://x", "", 2},
		{"non-positive dims", "http://x", "m", 0},
	}
	for _, c := range cases {
		if _, err := openai.NewEmbedder(c.baseURL, "k", c.model, c.dims, openai.AuthBearer, nil); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: want ErrInvalidArgument, got %v", c.name, err)
		}
	}
}
