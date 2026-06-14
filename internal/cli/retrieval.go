package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
	"github.com/jmurray2011/lore/internal/limitio"
)

// maxAttachmentBytes caps a single attachment read. An attachment is base64-
// inflated into a provider request, so an oversized file is refused up front
// rather than ballooning memory and the request body. It is a var, not a const,
// only so tests can lower it.
var maxAttachmentBytes int64 = 64 << 20

func newQueryCmd(deps *Deps) *cobra.Command {
	var (
		k            int
		source       string
		where        []string
		explain      bool
		rerank       bool
		candidates   int
		budget       int
		hybrid       bool
		maxPerSource int
		mmr          bool
		mmrLambda    float64
		recency      bool
		halfLifeDays float64
		fromColl     string
		collFlags    []string
	)
	cmd := &cobra.Command{
		Use:   "query [collection] <query>",
		Short: "Retrieve the most similar chunks",
		Long: "Retrieve the chunks most similar to <query> from one or more collections.\n\n" +
			"Target several same-space collections with repeatable -c/--collection; their hits " +
			"merge into one ranked top-k, each tagged with its origin collection.\n\n" +
			"With --from-collection, the named collection's own stored vectors are used as the " +
			"queries (no re-embedding) and results are grouped by source chunk — for finding where " +
			"two collections overlap or diverge.",
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, err := domain.ParseWhere(where)
			if err != nil {
				return err
			}
			if fromColl != "" {
				if hybrid {
					return fmt.Errorf("%w: --hybrid cannot be combined with --from-collection (which queries by stored vectors)", domain.ErrInvalidArgument)
				}
				return runQueryFrom(cmd, deps, args, fromColl, collFlags, k, source, filter)
			}

			collections, queryText, err := resolveCollectionArgs(args, collFlags, "query", "query string")
			if err != nil {
				return err
			}
			queryText, err = argOrStdin(cmd, queryText)
			if err != nil {
				return err
			}

			hits, runnerUp, err := resolveHits(cmd, deps, collections, queryText, k, candidates, source, filter, rerank, explain, hybrid, mmr, mmrLambda, recency, halfLifeDays)
			if err != nil {
				return err
			}
			hits = domain.CapPerSource(hits, maxPerSource)
			hits, tokens := budgetTrim(hits, budget, deps.Tokens)

			views, md := hitViews(hits)
			if err := render(cmd, views, md); err != nil {
				return err
			}
			// query's stdout is the bare hit array (the synthesize contract), so the
			// budget report (and --explain diagnostics) go to stderr.
			if budget > 0 {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "lore: budget %d: returned %d chunks (~%d tokens)\n", budget, len(hits), tokens)
			}
			if explain {
				return writeQueryExplain(cmd, buildExplain(hits, runnerUp, nil))
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to retrieve (the final count after --rerank)")
	cmd.Flags().StringVar(&source, "source", "", "restrict to documents whose source matches this glob (e.g. '*.pdf')")
	cmd.Flags().StringArrayVar(&where, "where", nil, "filter to documents whose metadata matches this predicate, e.g. 'author=alice' or 'date>=2025-01-01' (repeatable; ANDed)")
	cmd.Flags().BoolVar(&explain, "explain", false, "print the score distribution (top-k + the best rejected candidate) to stderr")
	cmd.Flags().BoolVar(&rerank, "rerank", false, "two-stage retrieval: vector-search a wide pool, then rerank to the top -k")
	cmd.Flags().IntVar(&candidates, "rerank-candidates", 50, "size of the pre-rerank vector candidate pool (must be >= -k)")
	cmd.Flags().IntVar(&budget, "budget", 0, "cap the returned set to this many tokens (after ranking; trims within -k)")
	cmd.Flags().BoolVar(&hybrid, "hybrid", deps.RetrievalHybrid, "hybrid retrieval: fuse vector and BM25 keyword results (Reciprocal Rank Fusion)")
	cmd.Flags().IntVar(&maxPerSource, "max-per-source", 0, "cap the number of returned chunks per source document (0 = no cap)")
	cmd.Flags().BoolVar(&mmr, "mmr", false, "diversify results with Maximal Marginal Relevance (single-collection; not with --rerank)")
	cmd.Flags().Float64Var(&mmrLambda, "mmr-lambda", 0.5, "MMR relevance/diversity trade-off in [0,1] (1=pure relevance, 0=pure diversity)")
	cmd.Flags().BoolVar(&recency, "recency", false, "recency-aware ranking: re-rank a wider pool by relevance with a time decay (not with --rerank/--mmr)")
	cmd.Flags().Float64Var(&halfLifeDays, "half-life-days", 90, "recency half-life in days: a chunk this old keeps half its score (used with --recency)")
	cmd.Flags().StringVar(&fromColl, "from-collection", "", "use this collection's stored vectors as the queries (no re-embedding), grouping hits by source chunk")
	cmd.Flags().StringArrayVarP(&collFlags, "collection", "c", nil, "additional collection to search; repeatable (results merge across same-space collections)")
	return cmd
}

// runQueryFrom handles `query <target> --from-collection <source>`: the target
// is the sole positional, there is no query text (and no stdin), and -c is not
// allowed — the source collection's stored vectors are the queries.
func runQueryFrom(cmd *cobra.Command, deps *Deps, args []string, fromColl string, collFlags []string, k int, source string, filter domain.Predicate) error {
	if len(collFlags) > 0 {
		return fmt.Errorf("%w: --from-collection cannot be combined with -c/--collection", domain.ErrInvalidArgument)
	}
	if len(args) != 1 {
		return fmt.Errorf("%w: query --from-collection takes the target <collection> and no query text", domain.ErrInvalidArgument)
	}
	groups, err := deps.Query.QueryFrom(cmd.Context(), args[0], fromColl, k, source, filter)
	if err != nil {
		return err
	}
	views, md := fromQueryViews(groups)
	return render(cmd, views, md)
}

// budgetTrim limits ranked hits to a cumulative token budget, applied after
// ranking (and after any rerank), returning the trimmed hits and their token
// total. budget <= 0 (or no counter) is a no-op. The first hit is always kept so
// a budget smaller than the top chunk still returns something; the caller has
// already capped the slice to -k, so budget only ever tightens the bound.
func budgetTrim(hits []domain.ChunkHit, budget int, counter app.TokenCounter) ([]domain.ChunkHit, int) {
	if budget <= 0 || counter == nil {
		return hits, 0
	}
	var out []domain.ChunkHit
	total := 0
	for _, h := range hits {
		t := counter.Count(h.Chunk.Text)
		if len(out) > 0 && total+t > budget {
			break
		}
		out = append(out, h)
		total += t
	}
	return out, total
}

// mmrPoolMin is the minimum candidate pool fetched for MMR before it selects the
// final -k diverse hits (MMR adds nothing if the pool is no larger than k).
const mmrPoolMin = 50

// resolveHits performs retrieval for query and ask, over one or many collections:
// a plain top-k vector search, or — with --rerank — two-stage retrieval (a wide
// vector candidate pool reranked to the final top-k), or — with --mmr —
// diversified selection from a wider pool, or — with --recency — a time-decay
// re-rank of a wider pool. --recency, --mmr, and --rerank each reorder the pool
// and are mutually exclusive. With more than one collection the
// candidates are merged across them by score (each carrying its origin
// collection). It returns the hits plus, for --explain, the runner-up score (the
// best candidate just outside the returned set, by whichever ordering is in
// effect — rerank score when reranking, similarity otherwise).
func resolveHits(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k, candidates int, source string, filter domain.Predicate, rerank, explain, hybrid, mmr bool, mmrLambda float64, recency bool, halfLifeDays float64) ([]domain.ChunkHit, *float64, error) {
	if recency {
		if mmr {
			return nil, nil, fmt.Errorf("%w: --recency and --mmr are mutually exclusive (both reorder the candidate pool)", domain.ErrInvalidArgument)
		}
		return resolveRecency(cmd, deps, collections, queryText, k, source, filter, rerank, hybrid, halfLifeDays)
	}
	if mmr {
		return resolveMMR(cmd, deps, collections, queryText, k, source, filter, rerank, hybrid, mmrLambda)
	}
	if rerank {
		if deps.Rerank == nil {
			return nil, nil, errRerankUnconfigured()
		}
		if candidates < k {
			return nil, nil, fmt.Errorf("%w: --rerank-candidates (%d) must be >= -k (%d)", domain.ErrInvalidArgument, candidates, k)
		}
		// --hybrid feeds the rerank pool: fuse a wide vector+lexical candidate set,
		// then the cross-encoder reorders it to the final top-k.
		pool, err := queryHits(cmd, deps, collections, queryText, candidates, source, filter, hybrid)
		if err != nil {
			return nil, nil, err
		}
		// With --explain, rerank the whole pool so the runner-up (k+1th by rerank
		// score) is visible; otherwise truncate to k in the use case.
		topN := k
		if explain {
			topN = 0
		}
		reranked, err := deps.Rerank.Rerank(cmd.Context(), queryText, pool, topN)
		if err != nil {
			return nil, nil, err
		}
		var runnerUp *float64
		if explain && k > 0 && len(reranked) > k {
			if rs := reranked[k].RerankScore; rs != nil {
				s := *rs
				runnerUp = &s
			}
			reranked = reranked[:k]
		}
		return reranked, runnerUp, nil
	}
	if explain {
		ret, err := explainHits(cmd, deps, collections, queryText, k, source, filter, hybrid)
		if err != nil {
			return nil, nil, err
		}
		return ret.Hits, nextScorePtr(ret), nil
	}
	hits, err := queryHits(cmd, deps, collections, queryText, k, source, filter, hybrid)
	return hits, nil, err
}

// queryHits retrieves the top-k hits for one collection (the byte-for-byte
// legacy path) or merges across several same-space collections.
func queryHits(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k int, source string, filter domain.Predicate, hybrid bool) ([]domain.ChunkHit, error) {
	if len(collections) > 1 {
		if hybrid {
			return nil, errHybridMultiCollection()
		}
		return deps.Query.QueryAcross(cmd.Context(), collections, queryText, k, source, filter)
	}
	if hybrid {
		return deps.Query.QueryHybrid(cmd.Context(), collections[0], queryText, k, source, filter)
	}
	return deps.Query.Query(cmd.Context(), collections[0], queryText, k, source, filter)
}

// explainHits is queryHits' --explain twin: it also surfaces the runner-up just
// outside the returned top-k, single- or multi-collection.
func explainHits(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k int, source string, filter domain.Predicate, hybrid bool) (app.Retrieval, error) {
	if len(collections) > 1 {
		if hybrid {
			return app.Retrieval{}, errHybridMultiCollection()
		}
		return deps.Query.ExplainAcross(cmd.Context(), collections, queryText, k, source, filter)
	}
	if hybrid {
		return deps.Query.ExplainHybrid(cmd.Context(), collections[0], queryText, k, source, filter)
	}
	return deps.Query.Explain(cmd.Context(), collections[0], queryText, k, source, filter)
}

// resolveMMR retrieves a wider candidate pool, then selects the final -k by
// Maximal Marginal Relevance for diversity. It is single-collection and mutually
// exclusive with --rerank (both reorder the candidate pool); it composes with
// --hybrid (the pool can be fused), --where, and --source. The candidate vectors
// come from the (existing) VectorIndex.Entries — the MMR redundancy penalty needs
// them; relevance is each hit's cosine Score.
func resolveMMR(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k int, source string, filter domain.Predicate, rerank, hybrid bool, lambda float64) ([]domain.ChunkHit, *float64, error) {
	if rerank {
		return nil, nil, fmt.Errorf("%w: --mmr and --rerank are mutually exclusive (both reorder the candidate pool)", domain.ErrInvalidArgument)
	}
	if len(collections) > 1 {
		return nil, nil, fmt.Errorf("%w: --mmr does not support multiple collections (-c); query each separately", domain.ErrInvalidArgument)
	}
	if deps.Index == nil {
		return nil, nil, fmt.Errorf("%w: --mmr needs the vector index, which is not available", domain.ErrInvalidArgument)
	}
	pool := k
	if pool < mmrPoolMin {
		pool = mmrPoolMin
	}
	hits, err := queryHits(cmd, deps, collections, queryText, pool, source, filter, hybrid)
	if err != nil {
		return nil, nil, err
	}
	entries, err := deps.Index.Entries(cmd.Context(), collections[0])
	if err != nil {
		return nil, nil, fmt.Errorf("read vectors for --mmr: %w", err)
	}
	vecByID := make(map[domain.ChunkID][]float32, len(entries))
	for _, e := range entries {
		vecByID[e.ChunkID] = e.Vector
	}
	cands := make([]domain.MMRCandidate, len(hits))
	for i, h := range hits {
		cands[i] = domain.MMRCandidate{Hit: h, Vector: vecByID[h.Chunk.ID]}
	}
	return domain.SelectMMR(cands, lambda, k), nil, nil
}

// recencyPoolMin is the minimum candidate pool fetched for --recency before the
// time-decay re-rank picks the final -k, so a fresh-but-slightly-less-relevant
// chunk that missed cosine-top-k can still surface.
const recencyPoolMin = 50

// resolveRecency retrieves a wider candidate pool, then re-ranks it by relevance
// blended with a time decay (domain.DecayByRecency) and trims to -k. It needs no
// vectors (unlike MMR), so it composes with --hybrid, --where, --source, and
// multiple collections; it is mutually exclusive with --rerank (both reorder the
// pool). The decay timestamp is each hit's document date metadata, then ingest
// time (domain.HitTime); now is the wall clock, passed into the pure transform.
func resolveRecency(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k int, source string, filter domain.Predicate, rerank, hybrid bool, halfLifeDays float64) ([]domain.ChunkHit, *float64, error) {
	if rerank {
		return nil, nil, fmt.Errorf("%w: --recency and --rerank are mutually exclusive (both reorder the candidate pool)", domain.ErrInvalidArgument)
	}
	if halfLifeDays <= 0 {
		return nil, nil, fmt.Errorf("%w: --half-life-days must be > 0, got %v", domain.ErrInvalidArgument, halfLifeDays)
	}
	pool := k
	if pool < recencyPoolMin {
		pool = recencyPoolMin
	}
	hits, err := queryHits(cmd, deps, collections, queryText, pool, source, filter, hybrid)
	if err != nil {
		return nil, nil, err
	}
	halfLife := time.Duration(halfLifeDays * 24 * float64(time.Hour))
	return domain.DecayByRecency(hits, halfLife, time.Now(), k), nil, nil
}

// verificationViews builds the --json per-claim verdicts for ask --verify.
func verificationViews(claims []domain.Claim) []verificationClaimView {
	out := make([]verificationClaimView, len(claims))
	for i, c := range claims {
		ids := make([]string, len(c.CitedChunks))
		for j, id := range c.CitedChunks {
			ids[j] = string(id)
		}
		out[i] = verificationClaimView{Claim: c.Text, CitedChunks: ids, Verdict: string(c.Verdict), Rationale: c.Rationale}
	}
	return out
}

// verificationMarkdown renders the human verification block: a support-rate header
// and each claim marked by verdict (✓ supported, ✗ unsupported, ? uncited).
func verificationMarkdown(claims []domain.Claim, supportRate float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Verification\n\n**Support rate: %.0f%%** (%d/%d claims supported)\n\n", supportRate*100, supportedCount(claims), len(claims))
	mark := map[domain.ClaimVerdict]string{
		domain.VerdictSupported:   "✓",
		domain.VerdictUnsupported: "✗",
		domain.VerdictUncited:     "?",
	}
	for _, c := range claims {
		fmt.Fprintf(&b, "- %s %s\n", mark[c.Verdict], c.Text)
		if c.Verdict == domain.VerdictUnsupported && c.Rationale != "" {
			fmt.Fprintf(&b, "  - _%s_\n", c.Rationale)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// supportedCount counts the supported claims.
func supportedCount(claims []domain.Claim) int {
	n := 0
	for _, c := range claims {
		if c.Verdict == domain.VerdictSupported {
			n++
		}
	}
	return n
}

// errHybridMultiCollection is the usage error (exit 2) for combining --hybrid with
// multiple collections, which v1.0 does not support (RRF fusion is per-collection;
// a cross-collection fused merge is a follow-up).
func errHybridMultiCollection() error {
	return fmt.Errorf("%w: --hybrid does not support multiple collections (-c); query each collection separately", domain.ErrInvalidArgument)
}

// resolveCollectionArgs derives the target collection set and the query/question
// text from the positional args and any repeatable -c flags. The legacy form is
// `<collection> <text>` (two positionals, no -c); with -c the collection may be
// omitted, leaving just `<text>`. The positional collection (if present) and the
// -c values are unioned and deduplicated, so a single collection — whether a bare
// positional or one -c — routes through the unchanged single-collection path.
func resolveCollectionArgs(args, collFlags []string, cmdName, textName string) (collections []string, text string, err error) {
	switch {
	case len(args) == 2:
		collections = append(collections, args[0])
		text = args[1]
	case len(args) == 1 && len(collFlags) > 0:
		text = args[0]
	default:
		return nil, "", fmt.Errorf("%w: %s takes a %s and at least one collection (a positional <collection> or -c/--collection)", domain.ErrInvalidArgument, cmdName, textName)
	}
	collections = dedupStrings(append(collections, collFlags...))
	return collections, text, nil
}

// dedupStrings returns the input with duplicates removed, preserving first-seen
// order.
func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// errRerankUnconfigured is the usage error (exit 2) for requesting reranking
// without a configured rerank provider, naming the config to set.
func errRerankUnconfigured() error {
	return fmt.Errorf("%w: reranking is not configured; set rerank.base_url and rerank.model (or LORE_RERANK_BASE_URL / LORE_RERANK_MODEL)", domain.ErrInvalidArgument)
}

// hitViews builds the JSON views and the shared human Markdown body for a list
// of hits (query and rerank). When a hit carries a rerank score, both scores are
// shown; otherwise the rendering is exactly query's original single-score form.
func hitViews(hits []domain.ChunkHit) ([]hitView, string) {
	views := make([]hitView, len(hits))
	var b strings.Builder
	for i, h := range hits {
		views[i] = hitView{ChunkID: string(h.Chunk.ID), Source: h.Source, Seq: h.Chunk.Seq, Score: h.Score, RerankScore: h.RerankScore, Collection: h.Collection, Metadata: h.Metadata, Text: h.Chunk.Text}
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		score := fmt.Sprintf("`%.4f`", h.Score)
		if h.RerankScore != nil {
			score = fmt.Sprintf("sim=`%.4f` rerank=`%.4f`", h.Score, *h.RerankScore)
		}
		fmt.Fprintf(&b, "**[%d]**  %s  ·  %s\n\n%s\n", i+1, hitLabel(h), score, h.Chunk.Text)
	}
	return views, strings.TrimRight(b.String(), "\n")
}

// fromQueryViews builds the JSON groups and human Markdown for query
// --from-collection: one block per source chunk, headed by the source chunk's
// provenance, with its target hits in the standard hit format.
func fromQueryViews(groups []app.FromQuery) ([]fromGroupView, string) {
	views := make([]fromGroupView, len(groups))
	var b strings.Builder
	for i, g := range groups {
		hv, hmd := hitViews(g.Hits)
		views[i] = fromGroupView{
			From: fromRef{ChunkID: string(g.From.Chunk.ID), Source: g.From.Source, Seq: g.From.Chunk.Seq},
			Hits: hv,
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "**from** %s\n\n", fromLabel(g.From))
		if hmd == "" {
			b.WriteString("_no matching chunks_")
		} else {
			b.WriteString(hmd)
		}
	}
	return views, b.String()
}

// writeQueryExplain emits the explain diagnostics to stderr: a JSON {explain:...}
// object under --json, otherwise the human text block. Stderr keeps query's
// stdout (the hit array) clean for the synthesize pipe.
func writeQueryExplain(cmd *cobra.Command, ev explainView) error {
	w := cmd.ErrOrStderr()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Explain explainView `json:"explain"`
		}{ev})
	}
	_, err := io.WriteString(w, explainText(ev))
	return err
}

// nextScorePtr returns the runner-up score as a pointer, or nil when there was
// no further candidate (so --json renders next_score: null).
func nextScorePtr(ret app.Retrieval) *float64 {
	if !ret.HasNext {
		return nil
	}
	s := ret.NextScore
	return &s
}

func newAskCmd(deps *Deps) *cobra.Command {
	var (
		k            int
		attach       []string
		strict       bool
		source       string
		where        []string
		expand       bool
		explain      bool
		rerank       bool
		candidates   int
		budget       int
		hybrid       bool
		maxPerSource int
		mmr          bool
		mmrLambda    float64
		recency      bool
		halfLifeDays float64
		verify       bool
		verifyStrict bool
		collFlags    []string
		streamFlag   bool
		noStreamFlag bool
	)
	cmd := &cobra.Command{
		Use:   "ask [collection] <question>",
		Short: "Answer a question grounded in the collection's chunks",
		Long: "Synthesize an answer to <question> grounded in retrieved chunks.\n\n" +
			"Target several same-space collections with repeatable -c/--collection; their hits " +
			"merge into one ranked grounding set and citations identify each chunk's origin " +
			"collection. Composes with --rerank, --budget, and --explain over the merged set.",
		RunE: func(cmd *cobra.Command, args []string) error {
			attachments, err := loadAttachments(attach)
			if err != nil {
				return err
			}
			filter, err := domain.ParseWhere(where)
			if err != nil {
				return err
			}
			collections, question, err := resolveCollectionArgs(args, collFlags, "ask", "question")
			if err != nil {
				return err
			}
			question, err = argOrStdin(cmd, question)
			if err != nil {
				return err
			}
			stream, err := wantStream(cmd, streamFlag, noStreamFlag)
			if err != nil {
				return err
			}
			if verifyStrict {
				verify = true // --verify-strict implies verification
			}
			if verify {
				if deps.Verify == nil {
					return fmt.Errorf("%w: verification is not available", domain.ErrInvalidArgument)
				}
				stream = false // verification needs the whole answer; cannot verify a token stream
			}
			multi := len(collections) > 1
			collLabel := strings.Join(collections, ", ")
			var (
				ans         app.Answer
				ret         app.Retrieval
				runnerUp    *float64
				groundTok   *int
				streamed    bool
				streamedRaw string
			)
			switch {
			case rerank || budget > 0 || multi || stream || hybrid || mmr || recency || maxPerSource > 0:
				// Interpose between retrieval and synthesis: resolve hits (across
				// collections, two-stage via --rerank, and/or --hybrid fusion), cap
				// them to the token --budget, then synthesize — streaming the prose
				// when enabled. Uses the Synthesize seam and replicates Ask's strict
				// guard, which it lacks.
				var hits []domain.ChunkHit
				hits, runnerUp, err = resolveHits(cmd, deps, collections, question, k, candidates, source, filter, rerank, explain, hybrid, mmr, mmrLambda, recency, halfLifeDays)
				if err != nil {
					return err
				}
				hits = domain.CapPerSource(hits, maxPerSource)
				if budget > 0 {
					var tokens int
					hits, tokens = budgetTrim(hits, budget, deps.Tokens)
					groundTok = &tokens
				}
				if len(hits) == 0 && len(attachments) == 0 && strict {
					return fmt.Errorf("ask %q: %w: no chunks matched and no attachments supplied", collLabel, app.ErrNoGrounding)
				}
				if stream {
					var raw strings.Builder
					w := cmd.OutOrStdout()
					ans, err = deps.Ask.SynthesizeStream(cmd.Context(), question, hits, attachments, func(tok string) {
						raw.WriteString(tok)
						_, _ = io.WriteString(w, tok)
					})
					streamed = true
					streamedRaw = raw.String()
				} else {
					ans, err = deps.Ask.Synthesize(cmd.Context(), question, hits, attachments)
				}
				ret.Hits = hits
			case explain:
				ans, ret, err = deps.Ask.AskExplain(cmd.Context(), collections[0], question, k, attachments, strict, source, filter)
				runnerUp = nextScorePtr(ret)
			default:
				ans, err = deps.Ask.Ask(cmd.Context(), collections[0], question, k, attachments, strict, source, filter)
			}
			if err != nil {
				return err // strict's ErrNoGrounding short-circuits here, before any expansion or explain output
			}
			if !ans.Grounded {
				// Non-strict path: the answer rests on model knowledge alone.
				// Warn on stderr so pipes keep clean stdout while a human or CI
				// log still sees it. --strict turns this into an error instead.
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"lore: warning: no chunks matched and no attachments; the answer for %q is not grounded (use --strict to fail instead)\n", collLabel)
			}
			if streamed {
				// The prose is already on stdout; append the sources (and, under
				// --explain, the stderr diagnostics) and we are done. --json never
				// streams, so there is no JSON view to build here.
				return finishStreamed(cmd, ans, ret.Hits, streamedRaw, expand, explain, runnerUp)
			}
			citations := make([]citationView, len(ans.Citations))
			for i, c := range ans.Citations {
				citations[i] = citationView{ChunkID: string(c.ChunkID), Source: c.Source, Seq: c.Seq, Collection: c.Collection}
			}
			view := answerView{Text: ans.Text, Citations: citations, Grounded: ans.Grounded, GroundingTokens: groundTok}
			if budget > 0 && groundTok != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "lore: budget %d: grounded on %d chunks (~%d tokens)\n", budget, len(ret.Hits), *groundTok)
			}

			// Base human output: the answer, optionally with each cited chunk's
			// full text appended (--expand), numbered with the answer's ordinals.
			md := answerMarkdown(ans)
			if expand {
				textByChunk := map[domain.ChunkID]string{}
				if ids := citedChunkIDs(ans); len(ids) > 0 {
					chunks, err := expansionChunks(cmd.Context(), deps.Catalog, ans.Citations, collections, multi)
					if err != nil {
						return err
					}
					view.Expansions = make([]chunkView, len(chunks))
					for i, c := range chunks {
						textByChunk[c.ID] = c.Text
						view.Expansions[i] = chunkView{ChunkID: string(c.ID), Seq: c.Seq, Text: c.Text}
					}
				}
				md = expandedSources(ans, textByChunk)
			}

			// --verify: check each claim's faithfulness against its cited chunks. The
			// per-claim verdicts ride in --json and append a human block; --verify-strict
			// turns any unsupported claim into a non-zero exit (gateErr, code 5),
			// returned only after the report is rendered so CI sees what failed.
			var gateErr error
			if verify {
				claims, err := deps.Verify.Verify(cmd.Context(), collections[0], ans)
				if err != nil {
					return err
				}
				view.Verification = verificationViews(claims)
				sr := domain.SupportRate(claims)
				view.SupportRate = &sr
				md += "\n\n" + verificationMarkdown(claims, sr)
				if verifyStrict && !domain.AllSupported(claims) {
					gateErr = fmt.Errorf("ask %q: %w: %d/%d claims supported", collLabel, app.ErrGateUnmet, supportedCount(claims), len(claims))
				}
			}

			// --explain: report the score distribution of the chunks that grounded
			// the answer, plus the runner-up, annotated with which were cited.
			// Orthogonal to --expand and --source. JSON carries it inside the
			// answer object; human output puts it on stderr so a piped answer
			// (stdout) stays clean.
			if explain {
				ev := buildExplain(ret.Hits, runnerUp, citedSet(ans))
				if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
					view.Explain = &ev
					if err := render(cmd, view, md); err != nil {
						return err
					}
					return gateErr
				}
				if err := render(cmd, view, md); err != nil {
					return err
				}
				if _, err := io.WriteString(cmd.ErrOrStderr(), explainText(ev)); err != nil {
					return err
				}
				return gateErr
			}

			if err := render(cmd, view, md); err != nil {
				return err
			}
			return gateErr
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to ground on (0 to ground on attachments only)")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "file to send to the model as an attachment (repeatable)")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail (exit 1) instead of answering when nothing grounds the question")
	cmd.Flags().StringVar(&source, "source", "", "restrict grounding to documents whose source matches this glob (e.g. '*.pdf')")
	cmd.Flags().StringArrayVar(&where, "where", nil, "restrict grounding to documents whose metadata matches this predicate, e.g. 'author=alice' (repeatable; ANDed)")
	cmd.Flags().BoolVar(&expand, "expand", false, "append the full text of each cited chunk after the answer")
	cmd.Flags().BoolVar(&explain, "explain", false, "print the retrieval score distribution to stderr (the answer's explain key under --json)")
	cmd.Flags().BoolVar(&verify, "verify", false, "check each answer sentence is entailed by the chunk(s) it cites; report per-claim verdicts and a support rate")
	cmd.Flags().BoolVar(&verifyStrict, "verify-strict", false, "as --verify, but exit non-zero (5) if any claim is unsupported (a CI faithfulness gate)")
	cmd.Flags().BoolVar(&rerank, "rerank", false, "two-stage retrieval: vector-search a wide pool, then rerank to the top -k before synthesis")
	cmd.Flags().IntVar(&candidates, "rerank-candidates", 50, "size of the pre-rerank vector candidate pool (must be >= -k)")
	cmd.Flags().IntVar(&budget, "budget", 0, "cap grounding to this many tokens (after ranking/rerank; trims within -k)")
	cmd.Flags().BoolVar(&hybrid, "hybrid", deps.RetrievalHybrid, "hybrid retrieval: fuse vector and BM25 keyword results (Reciprocal Rank Fusion) before grounding")
	cmd.Flags().IntVar(&maxPerSource, "max-per-source", 0, "cap the number of grounding chunks per source document (0 = no cap)")
	cmd.Flags().BoolVar(&mmr, "mmr", false, "diversify grounding with Maximal Marginal Relevance (single-collection; not with --rerank)")
	cmd.Flags().Float64Var(&mmrLambda, "mmr-lambda", 0.5, "MMR relevance/diversity trade-off in [0,1] (1=pure relevance, 0=pure diversity)")
	cmd.Flags().BoolVar(&recency, "recency", false, "recency-aware grounding: re-rank a wider pool by relevance with a time decay (not with --rerank/--mmr)")
	cmd.Flags().Float64Var(&halfLifeDays, "half-life-days", 90, "recency half-life in days: a chunk this old keeps half its score (used with --recency)")
	cmd.Flags().StringArrayVarP(&collFlags, "collection", "c", nil, "additional collection to ground on; repeatable (merges across same-space collections)")
	cmd.Flags().BoolVar(&streamFlag, "stream", false, "stream the answer's tokens as they arrive (forced on; the default on an interactive terminal)")
	cmd.Flags().BoolVar(&noStreamFlag, "no-stream", false, "disable streaming; buffer and render the full answer (restores rich Markdown output)")
	return cmd
}

// wantStream decides whether to stream the answer: never under --json (it is
// structured data) or --no-stream; forced by --stream; otherwise on by default
// when stdout is an interactive terminal — mirroring the TTY detection used for
// rich rendering. --stream and --no-stream together is a usage error.
func wantStream(cmd *cobra.Command, stream, noStream bool) (bool, error) {
	if stream && noStream {
		return false, fmt.Errorf("%w: --stream and --no-stream are mutually exclusive", domain.ErrInvalidArgument)
	}
	if noStream {
		return false, nil
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return false, nil
	}
	if stream {
		return true, nil
	}
	return isTerminal(cmd.OutOrStdout()), nil
}

// finishStreamed completes a streamed answer. The prose is already on stdout, so
// it appends a Sources block keyed to the model's inline [n] ordinals (the
// streamed text keeps them — the buffered path's appearance-renumbering cannot
// apply to text already printed), optionally with each cited chunk's full text
// under --expand, and the --explain diagnostics on stderr.
func finishStreamed(cmd *cobra.Command, ans app.Answer, hits []domain.ChunkHit, raw string, expand, explain bool, runnerUp *float64) error {
	out := cmd.OutOrStdout()
	if src := streamSourcesBlock(raw, hits, expand); src != "" {
		_, _ = fmt.Fprintf(out, "\n\n%s\n", src)
	} else {
		_, _ = io.WriteString(out, "\n")
	}
	if explain {
		ev := buildExplain(hits, runnerUp, citedSet(ans))
		_, err := io.WriteString(cmd.ErrOrStderr(), explainText(ev))
		return err
	}
	return nil
}

// streamSourcesBlock renders the Sources for a streamed answer by mapping the
// model's inline [n] ordinals (1-based into hits, first-appearance order) to
// their sources — the streamed prose keeps those numbers. With expand it also
// prints each cited chunk's full text (taken from the hits, which carry it).
func streamSourcesBlock(raw string, hits []domain.ChunkHit, expand bool) string {
	seen := make(map[int]bool)
	var order []int
	for _, m := range citeRefRE.FindAllStringSubmatch(raw, -1) {
		for _, part := range strings.Split(m[1], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 || n > len(hits) || seen[n] {
				continue
			}
			seen[n] = true
			order = append(order, n)
		}
	}
	if len(order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Sources\n")
	for _, n := range order {
		h := hits[n-1]
		if expand {
			fmt.Fprintf(&b, "\n[%d] (%s) — %s#%d\n\n%s\n", n, shortLabel(h.Source), h.Source, h.Chunk.Seq, h.Chunk.Text)
		} else {
			fmt.Fprintf(&b, "[%d] %s · chunk %d\n", n, shortLabel(h.Source), h.Chunk.Seq)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// expansionChunks fetches the text of the answer's cited chunks for --expand.
// Single-collection answers query the one collection (byte-for-byte the prior
// behavior); cross-collection answers group the citations by their origin
// collection and query each, returning the chunks in citation order.
func expansionChunks(ctx context.Context, cat *app.Catalog, citations []domain.Citation, collections []string, multi bool) ([]domain.Chunk, error) {
	if !multi {
		ids := make([]string, len(citations))
		for i, c := range citations {
			ids[i] = string(c.ChunkID)
		}
		return cat.ChunksByIDs(ctx, collections[0], ids)
	}

	byColl := map[string][]string{}
	for _, c := range citations {
		byColl[c.Collection] = append(byColl[c.Collection], string(c.ChunkID))
	}
	found := make(map[domain.ChunkID]domain.Chunk, len(citations))
	for coll, ids := range byColl {
		chunks, err := cat.ChunksByIDs(ctx, coll, ids)
		if err != nil {
			return nil, err
		}
		for _, ch := range chunks {
			found[ch.ID] = ch
		}
	}
	out := make([]domain.Chunk, 0, len(citations))
	for _, c := range citations {
		if ch, ok := found[c.ChunkID]; ok {
			out = append(out, ch)
		}
	}
	return out, nil
}

// argOrStdin returns arg unchanged, or the trimmed contents of stdin when arg is
// "-", so query/ask can take their text from a pipe.
func argOrStdin(cmd *cobra.Command, arg string) (string, error) {
	if arg != "-" {
		return arg, nil
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// hitLabel renders a hit's provenance for human output: "file.docx · chunk 3"
// when the source is known, prefixed with the origin collection for
// cross-collection results, falling back to the opaque chunk ID otherwise.
func hitLabel(h domain.ChunkHit) string {
	if h.Source == "" {
		return string(h.Chunk.ID)
	}
	label := fmt.Sprintf("%s · chunk %d", shortLabel(h.Source), h.Chunk.Seq)
	if h.Collection != "" {
		label = h.Collection + " · " + label
	}
	return label
}

// fromLabel renders the source chunk heading a query --from-collection group:
// "source-basename#seq · chunkID", or the bare chunk ID when the source is
// unknown.
func fromLabel(h domain.ChunkHit) string {
	if h.Source == "" {
		return fmt.Sprintf("`%s`", h.Chunk.ID)
	}
	return fmt.Sprintf("`%s#%d`  ·  `%s`", shortLabel(h.Source), h.Chunk.Seq, h.Chunk.ID)
}

// loadAttachments reads each path into an Attachment, detecting its media type
// from the file extension. An undetectable extension is a usage error so the
// caller learns the file won't be understood rather than silently dropping it.
func loadAttachments(paths []string) ([]domain.Attachment, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	attachments := make([]domain.Attachment, 0, len(paths))
	for _, path := range paths {
		mediaType, _, _ := strings.Cut(mime.TypeByExtension(filepath.Ext(path)), ";")
		if mediaType == "" {
			return nil, fmt.Errorf("%w: cannot determine media type of %q from its extension", domain.ErrInvalidArgument, path)
		}
		data, err := readCappedFile(path, maxAttachmentBytes)
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", path, err)
		}
		a, err := domain.NewAttachment(mediaType, filepath.Base(path), data)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, nil
}

// readCappedFile reads the whole file at path, failing if it exceeds max bytes.
func readCappedFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return limitio.ReadAll(f, max)
}
