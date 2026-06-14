// Package openai adapts the Embedder and Generator ports to any
// OpenAI-compatible HTTP API (OpenAI, Ollama's /v1, vLLM, LM Studio,
// OpenRouter). Configuration is a base URL, an optional API key, and model
// names. The HTTP plumbing (POST, retry, auth) lives in
// the shared internal/adapters/httpjson package.
package openai

import "github.com/jmurray2011/lore/internal/adapters/httpjson"

// AuthStyle and its values alias the shared httpjson auth scheme so existing
// callers (the composition root, NewEmbedder/NewGenerator) keep using
// openai.AuthBearer / openai.AuthAPIKey unchanged.
type AuthStyle = httpjson.Auth

const (
	AuthBearer = httpjson.AuthBearer
	AuthAPIKey = httpjson.AuthAPIKey
)
