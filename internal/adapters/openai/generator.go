package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// compile-time port check
var _ app.Generator = (*Generator)(nil)

const systemPrompt = "You are a precise assistant. Answer the question using only the provided context chunks. " +
	"If the context is insufficient, say so plainly. Reference the chunk IDs you relied on."

// structuredInstruction is appended for providers that support JSON-schema
// output: it asks for the answer and the exact chunk IDs used, as typed fields.
const structuredInstruction = " Respond as JSON matching the schema: put the prose answer in \"answer\" and the " +
	"exact chunk IDs you relied on in \"citations\"."

// answerSchema is the strict JSON schema for structured synthesis. Strict mode
// requires every property listed in "required" and additionalProperties false.
var answerSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"answer":    map[string]any{"type": "string"},
		"citations": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required":             []string{"answer", "citations"},
	"additionalProperties": false,
}

// Generator synthesizes grounded answers via an OpenAI-compatible
// /chat/completions endpoint. When structured is set, it requests JSON-schema
// output and reads the model's own citations; otherwise it returns the plain
// text and cites the whole grounding set.
type Generator struct {
	client
	model      string
	structured bool
}

// NewGenerator constructs a Generator. structured enables JSON-schema output and
// must only be set for providers that support it (decision 19: a config-declared
// capability). A nil httpClient uses http.DefaultClient.
func NewGenerator(baseURL, apiKey, model string, structured bool, httpClient *http.Client) (*Generator, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("openai generator: %w: base URL is required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("openai generator: %w: model is required", domain.ErrInvalidArgument)
	}
	return &Generator{client: newClient(baseURL, apiKey, httpClient), model: model, structured: structured}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Synthesize asks the model to answer the question grounded in the given hits.
func (g *Generator) Synthesize(ctx context.Context, question string, hits []domain.ChunkHit) (app.Answer, error) {
	system := systemPrompt
	if g.structured {
		system += structuredInstruction
	}
	req := chatRequest{
		Model: g.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: userPrompt(question, hits)},
		},
	}
	if g.structured {
		req.ResponseFormat = &responseFormat{
			Type:       "json_schema",
			JSONSchema: &jsonSchema{Name: "grounded_answer", Strict: true, Schema: answerSchema},
		}
	}

	var resp chatResponse
	if err := g.post(ctx, "/chat/completions", req, &resp); err != nil {
		return app.Answer{}, err
	}
	if len(resp.Choices) == 0 {
		return app.Answer{}, fmt.Errorf("openai: chat completion returned no choices")
	}
	content := resp.Choices[0].Message.Content

	if g.structured {
		return parseStructured(content, hits)
	}
	return app.Answer{Text: strings.TrimSpace(content), Citations: groundingSet(hits)}, nil
}

// parseStructured reads the model's JSON answer and keeps only citations that
// name a chunk actually in the grounding set (preserving the model's order),
// guarding against hallucinated chunk IDs.
func parseStructured(content string, hits []domain.ChunkHit) (app.Answer, error) {
	var out struct {
		Answer    string   `json:"answer"`
		Citations []string `json:"citations"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return app.Answer{}, fmt.Errorf("openai: parse structured answer: %w", err)
	}

	valid := make(map[domain.ChunkID]bool, len(hits))
	for _, h := range hits {
		valid[h.Chunk.ID] = true
	}
	var citations []domain.ChunkID
	seen := make(map[domain.ChunkID]bool, len(out.Citations))
	for _, c := range out.Citations {
		id := domain.ChunkID(c)
		if valid[id] && !seen[id] {
			citations = append(citations, id)
			seen[id] = true
		}
	}
	return app.Answer{Text: strings.TrimSpace(out.Answer), Citations: citations}, nil
}

// groundingSet returns every hit's chunk ID — the stopgap citation set used when
// structured output is unavailable.
func groundingSet(hits []domain.ChunkHit) []domain.ChunkID {
	citations := make([]domain.ChunkID, len(hits))
	for i, h := range hits {
		citations[i] = h.Chunk.ID
	}
	return citations
}

func userPrompt(question string, hits []domain.ChunkHit) string {
	var b strings.Builder
	if len(hits) == 0 {
		b.WriteString("No context is available.\n\n")
	} else {
		b.WriteString("Context:\n")
		for _, h := range hits {
			fmt.Fprintf(&b, "[%s]\n%s\n\n", h.Chunk.ID, h.Chunk.Text)
		}
	}
	fmt.Fprintf(&b, "Question: %s", question)
	return b.String()
}
