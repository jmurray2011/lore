// Package openai adapts the Embedder and Generator ports to any
// OpenAI-compatible HTTP API (OpenAI, Ollama's /v1, vLLM, LM Studio,
// OpenRouter). Configuration is a base URL, an optional API key, and model
// names (DESIGN.md, decision 3).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AuthStyle selects how the API key is presented. Bearer
// (Authorization: Bearer <key>) is the OpenAI default; APIKey (api-key: <key>)
// is Azure OpenAI's scheme (decision 21).
type AuthStyle int

const (
	AuthBearer AuthStyle = iota
	AuthAPIKey
)

// client is the shared HTTP plumbing for the OpenAI-compatible endpoints.
type client struct {
	baseURL string
	apiKey  string
	auth    AuthStyle
	http    *http.Client
}

func newClient(baseURL, apiKey string, auth AuthStyle, httpClient *http.Client) client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		auth:    auth,
		http:    httpClient,
	}
}

// post marshals in as JSON to baseURL+path, then decodes a 2xx response into
// out. Non-2xx responses become an error carrying the status and a snippet of
// the body. The context governs the request lifetime.
func (c client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("openai: encode %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openai: build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		switch c.auth {
		case AuthAPIKey:
			req.Header.Set("api-key", c.apiKey)
		default:
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openai: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("openai: %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("openai: decode %s response: %w", path, err)
	}
	return nil
}
