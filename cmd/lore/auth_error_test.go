package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/httpjson"
)

func TestAuthGuidanceRewrites401(t *testing.T) {
	se := &httpjson.StatusError{Path: "/embeddings", Code: 401, Body: `{"error":{"message":"You didn't provide an API key"}}`}
	msg, ok := authGuidance(se, "/home/u/.config/lore/config.toml")
	if !ok {
		t.Fatal("want ok=true for HTTP 401")
	}
	for _, want := range []string{"authentication", "LORE_API_KEY", "/home/u/.config/lore/config.toml", "docs/configuration.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "You didn't provide an API key") {
		t.Errorf("message must not leak the raw provider body: %s", msg)
	}
}

func TestAuthGuidancePassesThroughOthers(t *testing.T) {
	if _, ok := authGuidance(&httpjson.StatusError{Path: "/x", Code: 500, Body: "boom"}, "/c"); ok {
		t.Error("HTTP 500 should not be treated as an auth failure")
	}
	if _, ok := authGuidance(errors.New("plain"), "/c"); ok {
		t.Error("a non-status error should not be treated as an auth failure")
	}
}
