package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/jmurray2011/lore/internal/adapters/httpjson"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// compile-time port check
var _ app.Generator = (*Generator)(nil)

const systemPrompt = "You are a precise assistant. Answer the question using only the provided context chunks. " +
	"Each chunk is labeled with a bracketed number and its source document. " +
	"If the context is insufficient, say so plainly. Cite the chunk numbers you relied on inline, in square brackets, e.g. [2] or [2, 5]."

// PromptVersion identifies the prompt/contract this Generator emits answers
// under. The composition root folds it into the answer-cache salt so a prompt
// change invalidates cached answers rather than serving ones the new prompt
// would not have produced. Bump it whenever systemPrompt/structuredInstruction
// or the citation contract changes meaningfully.
const PromptVersion = "1"

// citationRE matches bracketed citations like [2] or [2, 5] in answer prose. The
// captured group may hold several comma-separated numbers.
var citationRE = regexp.MustCompile(`\[([^\[\]]+)\]`)

// structuredInstruction is appended for providers that support JSON-schema
// output: it asks for the answer and the chunk numbers used, as typed fields.
const structuredInstruction = " Respond as JSON matching the schema: put the prose answer in \"answer\" and the " +
	"bracketed chunk numbers you relied on, as integers, in \"citations\"."

// answerSchema is the strict JSON schema for structured synthesis. Strict mode
// requires every property listed in "required" and additionalProperties false.
var answerSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"answer":    map[string]any{"type": "string"},
		"citations": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
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
	client *httpjson.Client
	model  string
	caps   Capabilities
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
	return &Generator{client: httpjson.NewClient(baseURL, apiKey, auth, httpClient), model: model, caps: caps}, nil
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
	if err := g.client.Post(ctx, "/chat/completions", req, &resp); err != nil {
		return app.Answer{}, err
	}
	if len(resp.Choices) == 0 {
		return app.Answer{}, fmt.Errorf("openai: chat completion returned no choices")
	}
	content := resp.Choices[0].Message.Content

	if g.caps.StructuredOutput {
		return parseStructured(content, hits)
	}
	// Rewrite the model's [n] ordinals to the canonical [chunkID] form and collect
	// the cited chunks; fall back to the whole grounding set when it cited nothing.
	text, citations := resolveCitations(strings.TrimSpace(content), hits)
	if len(citations) == 0 {
		citations = groundingSet(hits)
	}
	return app.Answer{Text: text, Citations: citations}, nil
}

// resolveCitations rewrites the model's bracketed ordinal references ([n], or
// [n, m]) into the canonical [chunkID] form the rest of lore expects, and
// returns the cited chunks as Citations in first-appearance order. Ordinals are
// 1-based indices into hits. A bracket whose contents are not all valid ordinals
// (e.g. "[CVE-2024-1]") is left untouched and contributes no citation.
func resolveCitations(text string, hits []domain.ChunkHit) (string, []domain.Citation) {
	var citations []domain.Citation
	seen := make(map[int]bool)

	rewritten := citationRE.ReplaceAllStringFunc(text, func(m string) string {
		parts := strings.Split(m[1:len(m)-1], ",")
		nums := make([]int, 0, len(parts))
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || n < 1 || n > len(hits) {
				return m // not an all-ordinal citation: leave it verbatim
			}
			nums = append(nums, n)
		}
		ids := make([]string, len(nums))
		for i, n := range nums {
			h := hits[n-1]
			ids[i] = string(h.Chunk.ID)
			if !seen[n] {
				citations = append(citations, citation(h))
				seen[n] = true
			}
		}
		return "[" + strings.Join(ids, ", ") + "]"
	})
	return rewritten, citations
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

// parseStructured reads the model's JSON answer. Citations are 1-based chunk
// ordinals; out-of-range numbers (hallucinations) are dropped. Any [n] ordinals
// the model also wrote inline in the prose are rewritten to [chunkID] and unioned
// into the citation set. Surviving citations carry their hit's source provenance.
func parseStructured(content string, hits []domain.ChunkHit) (app.Answer, error) {
	var out struct {
		Answer    string `json:"answer"`
		Citations []int  `json:"citations"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return app.Answer{}, fmt.Errorf("openai: parse structured answer: %w", err)
	}

	text, citations := resolveCitations(strings.TrimSpace(out.Answer), hits)
	seen := make(map[domain.ChunkID]bool, len(citations))
	for _, c := range citations {
		seen[c.ChunkID] = true
	}
	for _, n := range out.Citations {
		if n < 1 || n > len(hits) {
			continue
		}
		h := hits[n-1]
		if !seen[h.Chunk.ID] {
			citations = append(citations, citation(h))
			seen[h.Chunk.ID] = true
		}
	}
	return app.Answer{Text: text, Citations: citations}, nil
}

// groundingSet cites every hit — the stopgap citation set used when structured
// output is unavailable.
func groundingSet(hits []domain.ChunkHit) []domain.Citation {
	citations := make([]domain.Citation, len(hits))
	for i, h := range hits {
		citations[i] = citation(h)
	}
	return citations
}

// citation builds a Citation carrying a hit's chunk identity and source
// provenance.
func citation(h domain.ChunkHit) domain.Citation {
	return domain.Citation{ChunkID: h.Chunk.ID, Source: h.Source, Seq: h.Chunk.Seq}
}

func userPrompt(question string, hits []domain.ChunkHit) string {
	var b strings.Builder
	if len(hits) == 0 {
		b.WriteString("No context is available.\n\n")
	} else {
		b.WriteString("Context (cite the bracketed numbers you use):\n\n")
		for i, h := range hits {
			// A short ordinal the model can cite reliably, plus the source so it
			// can reason about provenance (which document a claim comes from).
			if label := sourceLabel(h.Source); label != "" {
				fmt.Fprintf(&b, "[%d] (%s)\n%s\n\n", i+1, label, h.Chunk.Text)
			} else {
				fmt.Fprintf(&b, "[%d]\n%s\n\n", i+1, h.Chunk.Text)
			}
		}
	}
	fmt.Fprintf(&b, "Question: %s", question)
	return b.String()
}

// sourceLabel reduces a source URI to a short, readable document name for the
// prompt (the basename, scheme and path stripped). Empty when there is no source.
func sourceLabel(uri string) string {
	if i := strings.Index(uri, "://"); i >= 0 {
		uri = uri[i+3:]
	}
	uri = strings.TrimRight(uri, "/")
	if uri == "" {
		return ""
	}
	return path.Base(uri)
}
