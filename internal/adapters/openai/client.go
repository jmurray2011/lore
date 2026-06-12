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
	"strconv"
	"strings"
	"time"
)

// Retry policy for transient provider failures (429 rate limits, 503).
const (
	maxRetries   = 6
	maxRetryWait = 60 * time.Second
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
// out. It retries transient rate-limit/availability responses (429, 503),
// honoring the Retry-After header and backing off, up to maxRetries; other
// non-2xx responses become an error carrying the status and a body snippet. The
// context governs the request lifetime and cancels any backoff.
func (c client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("openai: encode %s request: %w", path, err)
	}

	for attempt := 0; ; attempt++ {
		resp, err := c.do(ctx, path, body)
		if err != nil {
			return err
		}

		if retryable(resp.StatusCode) && attempt < maxRetries {
			wait := retryWait(resp, attempt)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048)) // drain for keep-alive
			_ = resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				continue
			}
		}
		return decode(resp, path, out)
	}
}

func (c client) do(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build %s request: %w", path, err)
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
		return nil, fmt.Errorf("openai: %s: %w", path, err)
	}
	return resp, nil
}

func decode(resp *http.Response, path string, out any) error {
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

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// retryWait honors a Retry-After header (delay-seconds or HTTP date), falling
// back to exponential backoff, clamped to maxRetryWait.
func retryWait(resp *http.Response, attempt int) time.Duration {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return clampWait(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(v); err == nil {
			return clampWait(time.Until(t))
		}
	}
	return clampWait(time.Duration(1<<uint(attempt)) * time.Second)
}

func clampWait(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d > maxRetryWait:
		return maxRetryWait
	default:
		return d
	}
}
