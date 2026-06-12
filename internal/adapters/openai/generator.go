package openai

import (
	"context"
	"encoding/base64"
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

// Capabilities declares what the configured provider supports beyond plain
// chat. They are config-declared (decision 19), not probed: a capability set
// here that the backend lacks surfaces as an error at call time.
type Capabilities struct {
	StructuredOutput bool // JSON-schema response_format
	ImageInput       bool // image attachments via image_url
	DocumentInput    bool // document attachments via a file part
}

// Generator synthesizes grounded answers via an OpenAI-compatible
// /chat/completions endpoint. With StructuredOutput it requests JSON-schema
// output and reads the model's own citations; otherwise it returns plain text
// and cites the whole grounding set. Attachments are encoded into the user
// message when the matching capability is set.
type Generator struct {
	client
	model string
	caps  Capabilities
}

// NewGenerator constructs a Generator. caps must reflect only what the provider
// actually supports (decision 19). auth selects the API-key scheme (AuthBearer
// for OpenAI, AuthAPIKey for Azure). A nil httpClient uses http.DefaultClient.
func NewGenerator(baseURL, apiKey, model string, caps Capabilities, auth AuthStyle, httpClient *http.Client) (*Generator, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("openai generator: %w: base URL is required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("openai generator: %w: model is required", domain.ErrInvalidArgument)
	}
	return &Generator{client: newClient(baseURL, apiKey, auth, httpClient), model: model, caps: caps}, nil
}

// chatMessage is a request message. Content is a string for plain text, or a
// []contentPart when the user message carries attachments.
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
	File     *filePart     `json:"file,omitempty"`
}

type imageURLPart struct {
	URL string `json:"url"`
}

type filePart struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data"`
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
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Synthesize asks the model to answer the question grounded in the given hits
// and any attachments.
func (g *Generator) Synthesize(ctx context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment) (app.Answer, error) {
	system := systemPrompt
	if g.caps.StructuredOutput {
		system += structuredInstruction
	}
	userContent, err := g.userContent(question, hits, attachments)
	if err != nil {
		return app.Answer{}, err
	}
	req := chatRequest{
		Model: g.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: userContent},
		},
	}
	if g.caps.StructuredOutput {
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

	if g.caps.StructuredOutput {
		return parseStructured(content, hits)
	}
	return app.Answer{Text: strings.TrimSpace(content), Citations: groundingSet(hits)}, nil
}

// userContent builds the user message: a plain string when there are no
// attachments, otherwise a multimodal parts array (prompt text + one part per
// attachment). It errors if an attachment needs a capability that is off.
func (g *Generator) userContent(question string, hits []domain.ChunkHit, attachments []domain.Attachment) (any, error) {
	prompt := userPrompt(question, hits)
	if len(attachments) == 0 {
		return prompt, nil
	}
	parts := []contentPart{{Type: "text", Text: prompt}}
	for _, a := range attachments {
		part, err := g.encodeAttachment(a)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// encodeAttachment turns an Attachment into a content part: images as image_url,
// everything else as a file part. Each is gated by the matching capability.
func (g *Generator) encodeAttachment(a domain.Attachment) (contentPart, error) {
	dataURL := "data:" + a.MediaType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
	if strings.HasPrefix(a.MediaType, "image/") {
		if !g.caps.ImageInput {
			return contentPart{}, fmt.Errorf("openai: %w: provider not configured for image input (set provider.image_input)", domain.ErrInvalidArgument)
		}
		return contentPart{Type: "image_url", ImageURL: &imageURLPart{URL: dataURL}}, nil
	}
	if !g.caps.DocumentInput {
		return contentPart{}, fmt.Errorf("openai: %w: provider not configured for document input (set provider.document_input)", domain.ErrInvalidArgument)
	}
	return contentPart{Type: "file", File: &filePart{Filename: a.Name, FileData: dataURL}}, nil
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
