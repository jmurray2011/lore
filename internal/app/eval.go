package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jmurray2011/lore/internal/domain"
)

// EvalSetVersion is the current eval-set format version. An eval file may begin
// with a header line {"version": N}; a version newer than this is rejected. Absent
// header means version 1 (the schema is otherwise self-describing by field name).
const EvalSetVersion = 1

// EvalCase is one question in an eval set with its optional expectations. At least
// one of ExpectedSources/ExpectedChunks enables retrieval metrics for the case;
// faithfulness (support rate) is computed when verification is requested.
type EvalCase struct {
	Question        string   `json:"question"`
	ExpectedSources []string `json:"expected_sources,omitempty"`
	ExpectedChunks  []string `json:"expected_chunks,omitempty"`
	ExpectedAnswer  string   `json:"expected_answer,omitempty"`
}

// ParseEvalSet reads a JSONL eval set: an optional first header line
// {"version": N}, then one EvalCase per line (blank lines ignored). A case with
// an empty question, a malformed line, a too-new version, or an empty set is
// ErrInvalidArgument.
func ParseEvalSet(r io.Reader) ([]EvalCase, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // tolerate long lines
	var cases []EvalCase
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if v, ok := parseEvalHeader(line); ok {
				if v > EvalSetVersion {
					return nil, fmt.Errorf("%w: eval set version %d, this lore understands up to %d", domain.ErrInvalidArgument, v, EvalSetVersion)
				}
				continue
			}
		}
		var ec EvalCase
		if err := json.Unmarshal([]byte(line), &ec); err != nil {
			return nil, fmt.Errorf("%w: eval set line: %v", domain.ErrInvalidArgument, err)
		}
		if strings.TrimSpace(ec.Question) == "" {
			return nil, fmt.Errorf("%w: eval set case has no question", domain.ErrInvalidArgument)
		}
		cases = append(cases, ec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read eval set: %w", err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("%w: eval set has no cases", domain.ErrInvalidArgument)
	}
	return cases, nil
}

// parseEvalHeader reports whether line is a version header ({"version": N} with no
// question field) and its version.
func parseEvalHeader(line string) (int, bool) {
	var h struct {
		Version  *int   `json:"version"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		return 0, false
	}
	if h.Version != nil && h.Question == "" {
		return *h.Version, true
	}
	return 0, false
}

// CaseResult is the per-question outcome: the retrieval metrics (when the case
// declared expectations) and, when verification ran, the answer's claims and
// support rate.
type CaseResult struct {
	Question     string
	HasRetrieval bool // the case declared expected_sources/chunks
	Recall       float64
	Precision    float64
	MRR          float64
	NDCG         float64
	HitRate      float64
	HasVerify    bool
	SupportRate  float64
	Claims       []domain.Claim
}

// EvalReport is the whole-set outcome: per-case results and the aggregate means
// over the applicable cases.
type EvalReport struct {
	K          int
	Cases      []CaseResult
	Aggregates map[string]float64 // metric name → mean (see metric name constants)
}

// Metric names used in aggregates and --fail-under thresholds.
const (
	MetricRecall      = "recall"
	MetricPrecision   = "precision"
	MetricMRR         = "mrr"
	MetricNDCG        = "ndcg"
	MetricHitRate     = "hit_rate"
	MetricSupportRate = "support_rate"
)

// Evaluator runs an eval set against a collection, computing retrieval metrics
// and — when verify is set — answer faithfulness, via the existing use cases.
type Evaluator struct {
	querier *Querier
	asker   *Asker
	checker *Checker
}

// NewEvaluator wires an Evaluator. asker and checker are needed only for
// verification; pass them (the CLI always wires them) so --verify works.
func NewEvaluator(querier *Querier, asker *Asker, checker *Checker) *Evaluator {
	return &Evaluator{querier: querier, asker: asker, checker: checker}
}

// Evaluate runs every case: retrieve top-k, score retrieval against the case's
// expectations, and — when verify — synthesize an answer and verify its claims.
// Retrieval metrics use expected_chunks when present (exact), else expected_sources.
func (e *Evaluator) Evaluate(ctx context.Context, collection string, cases []EvalCase, k int, verify bool) (EvalReport, error) {
	if verify && (e.asker == nil || e.checker == nil) {
		return EvalReport{}, fmt.Errorf("%w: verification is not available", domain.ErrInvalidArgument)
	}
	report := EvalReport{K: k, Aggregates: map[string]float64{}}
	var nRetrieval, nVerify int
	sums := map[string]float64{}

	for _, c := range cases {
		hits, err := e.querier.Query(ctx, collection, c.Question, k, "", domain.Predicate{})
		if err != nil {
			return EvalReport{}, fmt.Errorf("eval %q: %w", c.Question, err)
		}
		res := CaseResult{Question: c.Question}

		if relevant, retrieved, ok := relevanceFor(c, hits); ok {
			res.HasRetrieval = true
			res.Recall = domain.RecallAtK(retrieved, relevant, k)
			res.Precision = domain.PrecisionAtK(retrieved, relevant, k)
			res.MRR = domain.MRR(retrieved, relevant)
			res.NDCG = domain.NDCGAtK(retrieved, relevant, k)
			res.HitRate = domain.HitRate(retrieved, relevant, k)
			nRetrieval++
			sums[MetricRecall] += res.Recall
			sums[MetricPrecision] += res.Precision
			sums[MetricMRR] += res.MRR
			sums[MetricNDCG] += res.NDCG
			sums[MetricHitRate] += res.HitRate
		}

		if verify {
			ans, err := e.asker.Synthesize(ctx, c.Question, hits, nil)
			if err != nil {
				return EvalReport{}, fmt.Errorf("eval synthesize %q: %w", c.Question, err)
			}
			claims, err := e.checker.Verify(ctx, collection, ans)
			if err != nil {
				return EvalReport{}, fmt.Errorf("eval verify %q: %w", c.Question, err)
			}
			res.HasVerify = true
			res.Claims = claims
			res.SupportRate = domain.SupportRate(claims)
			nVerify++
			sums[MetricSupportRate] += res.SupportRate
		}

		report.Cases = append(report.Cases, res)
	}

	mean := func(name string, n int) {
		if n > 0 {
			report.Aggregates[name] = sums[name] / float64(n)
		}
	}
	mean(MetricRecall, nRetrieval)
	mean(MetricPrecision, nRetrieval)
	mean(MetricMRR, nRetrieval)
	mean(MetricNDCG, nRetrieval)
	mean(MetricHitRate, nRetrieval)
	mean(MetricSupportRate, nVerify)
	return report, nil
}

// relevanceFor builds the relevant set and the retrieved list for a case's
// metrics: expected_chunks (matched against retrieved chunk IDs) takes precedence
// over expected_sources (matched against retrieved source URIs). ok is false when
// the case declared no expectations (excluded from retrieval aggregates).
func relevanceFor(c EvalCase, hits []domain.ChunkHit) (relevant map[string]bool, retrieved []string, ok bool) {
	switch {
	case len(c.ExpectedChunks) > 0:
		relevant = toSet(c.ExpectedChunks)
		for _, h := range hits {
			retrieved = append(retrieved, string(h.Chunk.ID))
		}
		return relevant, retrieved, true
	case len(c.ExpectedSources) > 0:
		relevant = toSet(c.ExpectedSources)
		// expected_sources judges documents, not chunk positions: collapse the
		// per-chunk source list to distinct sources in first-appearance (best-rank)
		// order so a document with several retrieved chunks counts once. Without
		// this, recall and nDCG exceed their [0,1] range when one relevant document
		// supplies many of the top-k chunks.
		seen := make(map[string]bool, len(hits))
		for _, h := range hits {
			if !seen[h.Source] {
				seen[h.Source] = true
				retrieved = append(retrieved, h.Source)
			}
		}
		return relevant, retrieved, true
	default:
		return nil, nil, false
	}
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
