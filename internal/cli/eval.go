package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// evalReportView is the --json shape of an eval run.
type evalReportView struct {
	K          int                `json:"k"`
	Cases      []evalCaseView     `json:"cases"`
	Aggregates map[string]float64 `json:"aggregates"`
}

// evalCaseView is one question's metrics; fields are present only when applicable
// (retrieval metrics when the case declared expectations, support_rate under --verify).
type evalCaseView struct {
	Question    string   `json:"question"`
	Recall      *float64 `json:"recall,omitempty"`
	Precision   *float64 `json:"precision,omitempty"`
	MRR         *float64 `json:"mrr,omitempty"`
	NDCG        *float64 `json:"ndcg,omitempty"`
	HitRate     *float64 `json:"hit_rate,omitempty"`
	SupportRate *float64 `json:"support_rate,omitempty"`
}

// knownMetrics is the set of metric names valid in --fail-under (and reported).
var knownMetrics = map[string]bool{
	app.MetricRecall: true, app.MetricPrecision: true, app.MetricMRR: true,
	app.MetricNDCG: true, app.MetricHitRate: true, app.MetricSupportRate: true,
}

func newEvalCmd(deps *Deps) *cobra.Command {
	var (
		file         string
		k            int
		verify       bool
		failUnder    []string
		source       string
		where        []string
		rerank       bool
		candidates   int
		hybrid       bool
		mmr          bool
		mmrLambda    float64
		recency      bool
		halfLifeDays float64
		maxPerSource int
	)
	cmd := &cobra.Command{
		Use:   "eval <collection>",
		Short: "Evaluate retrieval (and, with --verify, answer faithfulness) over a question set",
		Long: "Run a JSONL eval set against a collection and report retrieval quality (recall, " +
			"precision, MRR, nDCG, hit-rate) and — with --verify — answer faithfulness (support rate). " +
			"Each line is {\"question\", \"expected_sources\"?, \"expected_chunks\"?}. With --fail-under " +
			"<metric>=<value> (repeatable) the command exits 5 when an aggregate falls below its threshold — a CI gate.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: eval takes exactly one collection name", domain.ErrInvalidArgument)
			}
			thresholds, err := parseThresholds(failUnder)
			if err != nil {
				return err
			}
			cases, err := readEvalSet(cmd, file)
			if err != nil {
				return err
			}
			filter, err := domain.ParseWhere(where)
			if err != nil {
				return err
			}
			// Evaluate the retrieval the user actually runs: the same Retriever and
			// options as query/ask, so eval metrics reflect the configured pipeline,
			// not a fixed cosine baseline.
			retrieve := func(ctx context.Context, question string) ([]domain.ChunkHit, error) {
				hits, _, rerr := deps.Retriever.Resolve(ctx, app.RetrieveOptions{
					Collections:  []string{args[0]},
					Query:        question,
					K:            k,
					Candidates:   candidates,
					Source:       source,
					Filter:       filter,
					Rerank:       rerank,
					Hybrid:       hybrid,
					MMR:          mmr,
					MMRLambda:    mmrLambda,
					Recency:      recency,
					HalfLife:     time.Duration(halfLifeDays * 24 * float64(time.Hour)),
					MaxPerSource: maxPerSource,
				})
				return hits, rerr
			}
			report, err := deps.Eval.Evaluate(cmd.Context(), args[0], cases, k, verify, retrieve)
			if err != nil {
				return err
			}
			if err := render(cmd, evalView(report), evalMarkdown(report, verify)); err != nil {
				return err
			}
			return gateThresholds(report, thresholds)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "eval-set JSONL file ('-' or empty reads stdin)")
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to retrieve per question")
	cmd.Flags().BoolVar(&verify, "verify", false, "also synthesize and verify each answer, reporting the support rate")
	cmd.Flags().StringArrayVar(&failUnder, "fail-under", nil, "exit 5 if an aggregate metric is below value, e.g. 'recall=0.8' (repeatable)")
	// Retrieval flags mirror query/ask so eval measures the configured pipeline.
	cmd.Flags().StringVar(&source, "source", "", "restrict retrieval to documents whose source matches this glob")
	cmd.Flags().StringArrayVar(&where, "where", nil, "restrict retrieval to documents whose metadata matches this predicate (repeatable; ANDed)")
	cmd.Flags().BoolVar(&rerank, "rerank", false, "evaluate two-stage retrieval (wide vector pool then cross-encoder rerank)")
	cmd.Flags().IntVar(&candidates, "rerank-candidates", 50, "pre-rerank vector candidate pool (must be >= -k)")
	cmd.Flags().BoolVar(&hybrid, "hybrid", deps.RetrievalHybrid, "evaluate hybrid (BM25 + vector) retrieval")
	cmd.Flags().BoolVar(&mmr, "mmr", false, "evaluate MMR-diversified retrieval (single-collection; not with --rerank)")
	cmd.Flags().Float64Var(&mmrLambda, "mmr-lambda", 0.5, "MMR relevance/diversity trade-off in [0,1]")
	cmd.Flags().BoolVar(&recency, "recency", false, "evaluate recency-aware retrieval (not with --rerank/--mmr)")
	cmd.Flags().Float64Var(&halfLifeDays, "half-life-days", 90, "recency half-life in days (with --recency)")
	cmd.Flags().IntVar(&maxPerSource, "max-per-source", 0, "cap retrieved chunks per source document (0 = no cap)")
	return cmd
}

// readEvalSet reads the eval set from a file path, or stdin when path is empty or "-".
func readEvalSet(cmd *cobra.Command, path string) ([]app.EvalCase, error) {
	r := cmd.InOrStdin()
	if path != "" && path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	return app.ParseEvalSet(r)
}

// parseThresholds parses repeatable --fail-under "metric=value" flags, validating
// the metric name against knownMetrics.
func parseThresholds(flags []string) (map[string]float64, error) {
	out := make(map[string]float64, len(flags))
	for _, f := range flags {
		name, valStr, ok := strings.Cut(f, "=")
		name = strings.TrimSpace(name)
		if !ok || !knownMetrics[name] {
			return nil, fmt.Errorf("%w: --fail-under %q must be <metric>=<value> with metric one of recall/precision/mrr/ndcg/hit_rate/support_rate", domain.ErrInvalidArgument, f)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(valStr), 64)
		if err != nil {
			return nil, fmt.Errorf("%w: --fail-under %q: %v", domain.ErrInvalidArgument, f, err)
		}
		out[name] = v
	}
	return out, nil
}

// gateThresholds checks each threshold against the report's aggregates: a metric
// that was not computed is a usage error (exit 2); a computed metric below its
// threshold is a gate failure (exit 5). Thresholds are checked in name order for a
// deterministic message.
func gateThresholds(report app.EvalReport, thresholds map[string]float64) error {
	names := make([]string, 0, len(thresholds))
	for n := range thresholds {
		names = append(names, n)
	}
	sort.Strings(names)

	var failures []string
	for _, name := range names {
		got, ok := report.Aggregates[name]
		if !ok {
			return fmt.Errorf("%w: --fail-under %s set but no %s was computed (need expectations, or --verify for support_rate)", domain.ErrInvalidArgument, name, name)
		}
		if got < thresholds[name] {
			failures = append(failures, fmt.Sprintf("%s %.4f < %.4f", name, got, thresholds[name]))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w: %s", app.ErrGateUnmet, strings.Join(failures, "; "))
	}
	return nil
}

func evalView(report app.EvalReport) evalReportView {
	v := evalReportView{K: report.K, Aggregates: report.Aggregates, Cases: make([]evalCaseView, len(report.Cases))}
	for i, c := range report.Cases {
		cv := evalCaseView{Question: c.Question}
		if c.HasRetrieval {
			cv.Recall, cv.Precision, cv.MRR, cv.NDCG, cv.HitRate = ptr(c.Recall), ptr(c.Precision), ptr(c.MRR), ptr(c.NDCG), ptr(c.HitRate)
		}
		if c.HasVerify {
			cv.SupportRate = ptr(c.SupportRate)
		}
		v.Cases[i] = cv
	}
	return v
}

func ptr(f float64) *float64 { return &f }

// evalMarkdown renders the human report: an aggregates table over the metrics that
// were computed, then the per-question scores.
func evalMarkdown(report app.EvalReport, verify bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Evaluation (k=%d, %d cases)\n\n", report.K, len(report.Cases))

	order := []string{app.MetricRecall, app.MetricPrecision, app.MetricMRR, app.MetricNDCG, app.MetricHitRate, app.MetricSupportRate}
	var rows [][]string
	for _, m := range order {
		if v, ok := report.Aggregates[m]; ok {
			rows = append(rows, []string{m, fmt.Sprintf("%.4f", v)})
		}
	}
	if len(rows) > 0 {
		b.WriteString(mdTable([]string{"Metric", "Mean"}, rows))
		b.WriteString("\n")
	} else {
		b.WriteString("_No metrics computed (cases declared no expectations" + verifyHint(verify) + ")._\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func verifyHint(verify bool) string {
	if verify {
		return ""
	}
	return "; pass --verify for support rate"
}
