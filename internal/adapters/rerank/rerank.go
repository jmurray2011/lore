// Package rerank adapts the app.RerankProvider port to a Cohere-style rerank
// HTTP API (POST /rerank with {model, query, documents, top_n} → {results:
// [{index, relevance_score}]}), the de-facto standard that Cohere, Jina, Voyage,
// and others conform to. It is a separate provider from the OpenAI-compatible
// embed/chat endpoint, reusing only the shared httpjson transport (auth, retry).
package rerank

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
var _ app.RerankProvider = (*Reranker)(nil)

// Reranker calls a Cohere-style /rerank endpoint.
type Reranker struct {
	client *httpjson.Client
	model  string
}

// NewReranker constructs a Reranker. baseURL and model are required (an
// unconfigured rerank provider is a usage error the caller surfaces). auth
// selects the API-key scheme; a nil httpClient uses http.DefaultClient.
func NewReranker(baseURL, apiKey, model string, auth httpjson.Auth, httpClient *http.Client) (*Reranker, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("rerank: %w: base URL is required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("rerank: %w: model is required", domain.ErrInvalidArgument)
	}
	return &Reranker{client: httpjson.NewClient(baseURL, apiKey, auth, httpClient), model: model}, nil
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank scores documents against query via the /rerank endpoint and returns the
// results best-first as the provider ordered them. topN is forwarded when set;
// the Reranker use case still owns final ordering/truncation. Empty documents
// makes no request.
func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topN int) ([]app.RankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	req := rerankRequest{Model: r.model, Query: query, Documents: documents}
	if topN > 0 {
		req.TopN = topN
	}
	var resp rerankResponse
	if err := r.client.Post(ctx, "/rerank", req, &resp); err != nil {
		return nil, fmt.Errorf("rerank request failed: %w", err)
	}
	out := make([]app.RankResult, len(resp.Results))
	for i, res := range resp.Results {
		out[i] = app.RankResult{Index: res.Index, Score: res.RelevanceScore}
	}
	return out, nil
}
