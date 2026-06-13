package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jmurray2011/lore/internal/adapters/httpjson"
	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// compile-time port check
var _ app.Embedder = (*Embedder)(nil)

// Embedder turns texts into vectors via an OpenAI-compatible /embeddings
// endpoint. Its EmbeddingSpace (model + dimensions) is fixed at construction so
// Space is a cheap, network-free check the use cases can run before any work.
type Embedder struct {
	client *httpjson.Client
	model  string
	space  domain.EmbeddingSpace
}

// NewEmbedder constructs an Embedder. dimensions pins the space the operator
// expects; Embed verifies the API returns vectors of that size. auth selects
// the API-key scheme (AuthBearer for OpenAI, AuthAPIKey for Azure). A nil
// httpClient uses http.DefaultClient.
func NewEmbedder(baseURL, apiKey, model string, dimensions int, auth AuthStyle, httpClient *http.Client) (*Embedder, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("openai embedder: %w: base URL is required", domain.ErrInvalidArgument)
	}
	space, err := domain.NewEmbeddingSpace(model, dimensions)
	if err != nil {
		return nil, err
	}
	return &Embedder{
		client: httpjson.NewClient(baseURL, apiKey, auth, httpClient),
		model:  model,
		space:  space,
	}, nil
}

// Space reports the embedding space this embedder produces.
func (e *Embedder) Space(context.Context) (domain.EmbeddingSpace, error) {
	return e.space, nil
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per input text, in input order. Empty input makes no
// request. Results are reordered by the API's reported index, and every vector
// is checked against the configured dimensionality.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var resp embeddingsResponse
	if err := e.client.Post(ctx, "/embeddings", embeddingsRequest{Model: e.model, Input: texts}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("openai: embeddings returned %d vectors for %d inputs", len(resp.Data), len(texts))
	}

	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai: embedding index %d out of range [0,%d)", d.Index, len(texts))
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("openai: duplicate embedding index %d", d.Index)
		}
		if len(d.Embedding) != e.space.Dimensions {
			return nil, fmt.Errorf("openai: embedding has %d dimensions, collection space wants %d", len(d.Embedding), e.space.Dimensions)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
