package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func newQueryCmd(deps *Deps) *cobra.Command {
	var (
		k          int
		source     string
		explain    bool
		rerank     bool
		candidates int
		budget     int
		fromColl   string
		collFlags  []string
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
			if fromColl != "" {
				return runQueryFrom(cmd, deps, args, fromColl, collFlags, k, source)
			}

			collections, queryText, err := resolveCollectionArgs(args, collFlags, "query", "query string")
			if err != nil {
				return err
			}
			queryText, err = argOrStdin(cmd, queryText)
			if err != nil {
				return err
			}

			hits, runnerUp, err := resolveHits(cmd, deps, collections, queryText, k, candidates, source, rerank, explain)
			if err != nil {
				return err
			}
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
	cmd.Flags().BoolVar(&explain, "explain", false, "print the score distribution (top-k + the best rejected candidate) to stderr")
	cmd.Flags().BoolVar(&rerank, "rerank", false, "two-stage retrieval: vector-search a wide pool, then rerank to the top -k")
	cmd.Flags().IntVar(&candidates, "rerank-candidates", 50, "size of the pre-rerank vector candidate pool (must be >= -k)")
	cmd.Flags().IntVar(&budget, "budget", 0, "cap the returned set to this many tokens (after ranking; trims within -k)")
	cmd.Flags().StringVar(&fromColl, "from-collection", "", "use this collection's stored vectors as the queries (no re-embedding), grouping hits by source chunk")
	cmd.Flags().StringArrayVarP(&collFlags, "collection", "c", nil, "additional collection to search; repeatable (results merge across same-space collections)")
	return cmd
}

// runQueryFrom handles `query <target> --from-collection <source>`: the target
// is the sole positional, there is no query text (and no stdin), and -c is not
// allowed — the source collection's stored vectors are the queries.
func runQueryFrom(cmd *cobra.Command, deps *Deps, args []string, fromColl string, collFlags []string, k int, source string) error {
	if len(collFlags) > 0 {
		return fmt.Errorf("%w: --from-collection cannot be combined with -c/--collection", domain.ErrInvalidArgument)
	}
	if len(args) != 1 {
		return fmt.Errorf("%w: query --from-collection takes the target <collection> and no query text", domain.ErrInvalidArgument)
	}
	groups, err := deps.Query.QueryFrom(cmd.Context(), args[0], fromColl, k, source)
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

// resolveHits performs retrieval for query and ask, over one or many collections:
// a plain top-k vector search, or — with --rerank — two-stage retrieval (a wide
// vector candidate pool reranked to the final top-k). With more than one
// collection the candidates are merged across them by score (each carrying its
// origin collection). It returns the hits plus, for --explain, the runner-up
// score (the best candidate just outside the returned set, by whichever ordering
// is in effect — rerank score when reranking, similarity otherwise).
func resolveHits(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k, candidates int, source string, rerank, explain bool) ([]domain.ChunkHit, *float64, error) {
	if rerank {
		if deps.Rerank == nil {
			return nil, nil, errRerankUnconfigured()
		}
		if candidates < k {
			return nil, nil, fmt.Errorf("%w: --rerank-candidates (%d) must be >= -k (%d)", domain.ErrInvalidArgument, candidates, k)
		}
		pool, err := queryHits(cmd, deps, collections, queryText, candidates, source)
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
		ret, err := explainHits(cmd, deps, collections, queryText, k, source)
		if err != nil {
			return nil, nil, err
		}
		return ret.Hits, nextScorePtr(ret), nil
	}
	hits, err := queryHits(cmd, deps, collections, queryText, k, source)
	return hits, nil, err
}

// queryHits retrieves the top-k hits for one collection (the byte-for-byte
// legacy path) or merges across several same-space collections.
func queryHits(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k int, source string) ([]domain.ChunkHit, error) {
	if len(collections) > 1 {
		return deps.Query.QueryAcross(cmd.Context(), collections, queryText, k, source)
	}
	return deps.Query.Query(cmd.Context(), collections[0], queryText, k, source)
}

// explainHits is queryHits' --explain twin: it also surfaces the runner-up just
// outside the returned top-k, single- or multi-collection.
func explainHits(cmd *cobra.Command, deps *Deps, collections []string, queryText string, k int, source string) (app.Retrieval, error) {
	if len(collections) > 1 {
		return deps.Query.ExplainAcross(cmd.Context(), collections, queryText, k, source)
	}
	return deps.Query.Explain(cmd.Context(), collections[0], queryText, k, source)
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
		views[i] = hitView{ChunkID: string(h.Chunk.ID), Source: h.Source, Seq: h.Chunk.Seq, Score: h.Score, RerankScore: h.RerankScore, Collection: h.Collection, Text: h.Chunk.Text}
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
		k          int
		attach     []string
		strict     bool
		source     string
		expand     bool
		explain    bool
		rerank     bool
		candidates int
		budget     int
		collFlags  []string
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
			collections, question, err := resolveCollectionArgs(args, collFlags, "ask", "question")
			if err != nil {
				return err
			}
			question, err = argOrStdin(cmd, question)
			if err != nil {
				return err
			}
			multi := len(collections) > 1
			collLabel := strings.Join(collections, ", ")
			var (
				ans       app.Answer
				ret       app.Retrieval
				runnerUp  *float64
				groundTok *int
			)
			switch {
			case rerank || budget > 0 || multi:
				// Interpose between retrieval and synthesis: resolve hits (across
				// collections and/or two-stage via --rerank), cap them to the token
				// --budget, then synthesize. Uses Synthesize (the documented seam) and
				// replicates Ask's strict guard, which Synthesize lacks.
				var hits []domain.ChunkHit
				hits, runnerUp, err = resolveHits(cmd, deps, collections, question, k, candidates, source, rerank, explain)
				if err != nil {
					return err
				}
				if budget > 0 {
					var tokens int
					hits, tokens = budgetTrim(hits, budget, deps.Tokens)
					groundTok = &tokens
				}
				if len(hits) == 0 && len(attachments) == 0 && strict {
					return fmt.Errorf("ask %q: %w: no chunks matched and no attachments supplied", collLabel, app.ErrNoGrounding)
				}
				ans, err = deps.Ask.Synthesize(cmd.Context(), question, hits, attachments)
				ret.Hits = hits
			case explain:
				ans, ret, err = deps.Ask.AskExplain(cmd.Context(), collections[0], question, k, attachments, strict, source)
				runnerUp = nextScorePtr(ret)
			default:
				ans, err = deps.Ask.Ask(cmd.Context(), collections[0], question, k, attachments, strict, source)
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

			// --explain: report the score distribution of the chunks that grounded
			// the answer, plus the runner-up, annotated with which were cited.
			// Orthogonal to --expand and --source. JSON carries it inside the
			// answer object; human output puts it on stderr so a piped answer
			// (stdout) stays clean.
			if explain {
				ev := buildExplain(ret.Hits, runnerUp, citedSet(ans))
				if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
					view.Explain = &ev
					return render(cmd, view, md)
				}
				if err := render(cmd, view, md); err != nil {
					return err
				}
				_, err := io.WriteString(cmd.ErrOrStderr(), explainText(ev))
				return err
			}

			return render(cmd, view, md)
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to ground on (0 to ground on attachments only)")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "file to send to the model as an attachment (repeatable)")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail (exit 1) instead of answering when nothing grounds the question")
	cmd.Flags().StringVar(&source, "source", "", "restrict grounding to documents whose source matches this glob (e.g. '*.pdf')")
	cmd.Flags().BoolVar(&expand, "expand", false, "append the full text of each cited chunk after the answer")
	cmd.Flags().BoolVar(&explain, "explain", false, "print the retrieval score distribution to stderr (the answer's explain key under --json)")
	cmd.Flags().BoolVar(&rerank, "rerank", false, "two-stage retrieval: vector-search a wide pool, then rerank to the top -k before synthesis")
	cmd.Flags().IntVar(&candidates, "rerank-candidates", 50, "size of the pre-rerank vector candidate pool (must be >= -k)")
	cmd.Flags().IntVar(&budget, "budget", 0, "cap grounding to this many tokens (after ranking/rerank; trims within -k)")
	cmd.Flags().StringArrayVarP(&collFlags, "collection", "c", nil, "additional collection to ground on; repeatable (merges across same-space collections)")
	return cmd
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
		data, err := os.ReadFile(path)
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
