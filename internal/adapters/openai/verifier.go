package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jmurray2011/lore/internal/adapters/httpjson"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// compile-time port check
var _ app.Verifier = (*Verifier)(nil)

// VerifierPromptVersion identifies the entailment prompt/contract. The composition
// root folds it into the verdict-cache salt so a prompt change invalidates cached
// verdicts. Bump it when verifierSystemPrompt or the verdict contract changes.
const VerifierPromptVersion = "1"

const verifierSystemPrompt = "You are a strict fact-checker. Given EVIDENCE and a CLAIM, decide whether the claim is " +
	"fully and directly supported by the evidence alone — do not use any outside knowledge. " +
	"Respond ONLY with a JSON object: {\"supported\": true or false, \"rationale\": \"<one short sentence>\"}."

// verdictSchema is the strict JSON schema for a structured verdict.
var verdictSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"supported": map[string]any{"type": "boolean"},
		"rationale": map[string]any{"type": "string"},
	},
	"required":             []string{"supported", "rationale"},
	"additionalProperties": false,
}

// Verifier checks claim/evidence entailment via an OpenAI-compatible
// /chat/completions endpoint, reusing the chat model and client. With
// structuredOutput it requests JSON-schema output; otherwise it parses the JSON
// object out of the model's text. It shares the chat connection (decision-60
// split endpoints), so verification runs against whatever serves chat.
type Verifier struct {
	client     *httpjson.Client
	model      string
	structured bool
}

// NewVerifier constructs a Verifier over the chat endpoint. structured should
// match the chat provider's StructuredOutput capability.
func NewVerifier(baseURL, apiKey, model string, structured bool, auth AuthStyle, httpClient *http.Client) (*Verifier, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("openai verifier: %w: base URL is required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("openai verifier: %w: model is required", domain.ErrInvalidArgument)
	}
	return &Verifier{client: httpjson.NewClient(baseURL, apiKey, auth, httpClient), model: model, structured: structured}, nil
}

// Verify asks the model whether evidence supports claim. An empty evidence string
// is treated as unsupported without a model call (nothing can entail a claim).
func (v *Verifier) Verify(ctx context.Context, claim, evidence string) (app.Verdict, error) {
	if strings.TrimSpace(evidence) == "" {
		return app.Verdict{Supported: false, Rationale: "no evidence"}, nil
	}
	req := chatRequest{
		Model: v.model,
		Messages: []chatMessage{
			{Role: "system", Content: verifierSystemPrompt},
			{Role: "user", Content: "EVIDENCE:\n" + evidence + "\n\nCLAIM:\n" + claim},
		},
	}
	if v.structured {
		req.ResponseFormat = &responseFormat{
			Type:       "json_schema",
			JSONSchema: &jsonSchema{Name: "verdict", Strict: true, Schema: verdictSchema},
		}
	}
	var resp chatResponse
	if err := v.client.Post(ctx, "/chat/completions", req, &resp); err != nil {
		return app.Verdict{}, err
	}
	if len(resp.Choices) == 0 {
		return app.Verdict{}, fmt.Errorf("openai: verifier returned no choices")
	}
	return parseVerdict(resp.Choices[0].Message.Content)
}

// parseVerdict extracts the {supported, rationale} JSON object from the model's
// reply, tolerating surrounding prose or code fences by taking the first balanced
// {...} span. If nothing parseable is found, the claim is treated as unsupported —
// the safe-to-be-wrong direction for an audit gate (flag, don't silently pass).
func parseVerdict(content string) (app.Verdict, error) {
	obj := firstJSONObject(content)
	if obj == "" {
		return app.Verdict{Supported: false, Rationale: "unparseable verdict"}, nil
	}
	var parsed struct {
		Supported bool   `json:"supported"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(obj), &parsed); err != nil {
		return app.Verdict{Supported: false, Rationale: "unparseable verdict"}, nil
	}
	return app.Verdict{Supported: parsed.Supported, Rationale: strings.TrimSpace(parsed.Rationale)}, nil
}

// firstJSONObject returns the substring from the first '{' to its matching '}',
// or "" if there is no balanced object.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
