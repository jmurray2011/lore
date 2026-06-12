package openai

import (
	"context"
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

// Generator synthesizes grounded answers via an OpenAI-compatible
// /chat/completions endpoint.
type Generator struct {
	client
	model string
}

// NewGenerator constructs a Generator. A nil httpClient uses http.DefaultClient.
func NewGenerator(baseURL, apiKey, model string, httpClient *http.Client) (*Generator, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("openai generator: %w: base URL is required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("openai generator: %w: model is required", domain.ErrInvalidArgument)
	}
	return &Generator{client: newClient(baseURL, apiKey, httpClient), model: model}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Synthesize asks the model to answer the question grounded in the given hits.
// The returned Answer cites the chunks supplied as context (the grounding set);
// extracting the model's inline citations precisely is a later refinement.
func (g *Generator) Synthesize(ctx context.Context, question string, hits []domain.ChunkHit) (app.Answer, error) {
	req := chatRequest{
		Model: g.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt(question, hits)},
		},
	}

	var resp chatResponse
	if err := g.post(ctx, "/chat/completions", req, &resp); err != nil {
		return app.Answer{}, err
	}
	if len(resp.Choices) == 0 {
		return app.Answer{}, fmt.Errorf("openai: chat completion returned no choices")
	}

	citations := make([]domain.ChunkID, len(hits))
	for i, h := range hits {
		citations[i] = h.Chunk.ID
	}
	return app.Answer{
		Text:      strings.TrimSpace(resp.Choices[0].Message.Content),
		Citations: citations,
	}, nil
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
