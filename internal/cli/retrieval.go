package cli

import (
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
	)
	cmd := &cobra.Command{
		Use:   "query <collection> <query>",
		Short: "Retrieve the most similar chunks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: query takes <collection> and a query string", domain.ErrInvalidArgument)
			}
			queryText, err := argOrStdin(cmd, args[1])
			if err != nil {
				return err
			}

			hits, runnerUp, err := resolveHits(cmd, deps, args[0], queryText, k, candidates, source, rerank, explain)
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
	return cmd
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

// resolveHits performs retrieval for query and ask: a plain top-k vector search,
// or — with --rerank — two-stage retrieval (a wide vector candidate pool reranked
// to the final top-k). It returns the hits plus, for --explain, the runner-up
// score (the best candidate just outside the returned set, by whichever ordering
// is in effect — rerank score when reranking, similarity otherwise).
func resolveHits(cmd *cobra.Command, deps *Deps, collection, queryText string, k, candidates int, source string, rerank, explain bool) ([]domain.ChunkHit, *float64, error) {
	if rerank {
		if deps.Rerank == nil {
			return nil, nil, errRerankUnconfigured()
		}
		if candidates < k {
			return nil, nil, fmt.Errorf("%w: --rerank-candidates (%d) must be >= -k (%d)", domain.ErrInvalidArgument, candidates, k)
		}
		pool, err := deps.Query.Query(cmd.Context(), collection, queryText, candidates, source)
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
		ret, err := deps.Query.Explain(cmd.Context(), collection, queryText, k, source)
		if err != nil {
			return nil, nil, err
		}
		return ret.Hits, nextScorePtr(ret), nil
	}
	hits, err := deps.Query.Query(cmd.Context(), collection, queryText, k, source)
	return hits, nil, err
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
		views[i] = hitView{ChunkID: string(h.Chunk.ID), Source: h.Source, Seq: h.Chunk.Seq, Score: h.Score, RerankScore: h.RerankScore, Text: h.Chunk.Text}
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
	)
	cmd := &cobra.Command{
		Use:   "ask <collection> <question>",
		Short: "Answer a question grounded in the collection's chunks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: ask takes <collection> and a question", domain.ErrInvalidArgument)
			}
			attachments, err := loadAttachments(attach)
			if err != nil {
				return err
			}
			question, err := argOrStdin(cmd, args[1])
			if err != nil {
				return err
			}
			var (
				ans       app.Answer
				ret       app.Retrieval
				runnerUp  *float64
				groundTok *int
			)
			switch {
			case rerank || budget > 0:
				// Interpose between retrieval and synthesis: resolve hits (optionally
				// two-stage via --rerank), cap them to the token --budget, then
				// synthesize. Uses Synthesize (the documented seam) and replicates
				// Ask's strict guard, which Synthesize lacks.
				var hits []domain.ChunkHit
				hits, runnerUp, err = resolveHits(cmd, deps, args[0], question, k, candidates, source, rerank, explain)
				if err != nil {
					return err
				}
				if budget > 0 {
					var tokens int
					hits, tokens = budgetTrim(hits, budget, deps.Tokens)
					groundTok = &tokens
				}
				if len(hits) == 0 && len(attachments) == 0 && strict {
					return fmt.Errorf("ask %q: %w: no chunks matched and no attachments supplied", args[0], app.ErrNoGrounding)
				}
				ans, err = deps.Ask.Synthesize(cmd.Context(), question, hits, attachments)
				ret.Hits = hits
			case explain:
				ans, ret, err = deps.Ask.AskExplain(cmd.Context(), args[0], question, k, attachments, strict, source)
				runnerUp = nextScorePtr(ret)
			default:
				ans, err = deps.Ask.Ask(cmd.Context(), args[0], question, k, attachments, strict, source)
			}
			if err != nil {
				return err // strict's ErrNoGrounding short-circuits here, before any expansion or explain output
			}
			if !ans.Grounded {
				// Non-strict path: the answer rests on model knowledge alone.
				// Warn on stderr so pipes keep clean stdout while a human or CI
				// log still sees it. --strict turns this into an error instead.
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"lore: warning: no chunks matched and no attachments; the answer for %q is not grounded (use --strict to fail instead)\n", args[0])
			}
			citations := make([]citationView, len(ans.Citations))
			for i, c := range ans.Citations {
				citations[i] = citationView{ChunkID: string(c.ChunkID), Source: c.Source, Seq: c.Seq}
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
					chunks, err := deps.Catalog.ChunksByIDs(cmd.Context(), args[0], ids)
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
	return cmd
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
// when the source is known, falling back to the opaque chunk ID otherwise.
func hitLabel(h domain.ChunkHit) string {
	if h.Source == "" {
		return string(h.Chunk.ID)
	}
	return fmt.Sprintf("%s · chunk %d", shortLabel(h.Source), h.Chunk.Seq)
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
