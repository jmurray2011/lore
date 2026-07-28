package httpjson_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/httpjson"
)

func TestPostReturnsStatusErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"no key"}}`)
	}))
	defer srv.Close()

	c := httpjson.NewClient(srv.URL, "", httpjson.AuthBearer, srv.Client())
	var out map[string]any
	err := c.Post(context.Background(), "/embeddings", map[string]any{"x": 1}, &out)

	var se *httpjson.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("want *StatusError, got %T: %v", err, err)
	}
	if se.Code != http.StatusUnauthorized {
		t.Fatalf("want status 401, got %d", se.Code)
	}
	if se.Path != "/embeddings" {
		t.Fatalf("want path /embeddings, got %q", se.Path)
	}
}
