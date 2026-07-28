package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func newSynthesizeCmd(deps *Deps) *cobra.Command {
	var (
		attach       []string
		verify       bool
		verifyStrict bool
		expand       bool
		streamFlag   bool
		noStreamFlag bool
	)
	cmd := &cobra.Command{
		Use:   "synthesize <question>",
		Short: "Synthesize an answer from chunk hits read as JSON on stdin",
		Long: "Read query hits (the JSON array `lore query --json` emits) from stdin and " +
			"synthesize an answer, letting you interpose your own filtering, re-ranking, or " +
			"merging — or supply chunks from an entirely different retriever — between " +
			"retrieval and synthesis:\n\n" +
			"  lore query kb \"...\" --json | jq 'map(select(.score > 0.3))' | lore synthesize \"...\"\n\n" +
			"Unlike `ask`, it performs no retrieval and has no --strict: the hits you pipe in are " +
			"the grounding. --verify checks the answer against those piped chunks (no collection " +
			"lookup), so faithfulness gating works even for externally-retrieved context.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: synthesize takes a question (hits come from stdin)", domain.ErrInvalidArgument)
			}
			question := args[0]
			attachments, err := loadAttachments(attach)
			if err != nil {
				return err
			}
			hits, err := readHits(cmd.InOrStdin())
			if err != nil {
				return err
			}

			if verifyStrict {
				verify = true // --verify-strict implies verification
			}
			stream, err := wantStream(cmd, streamFlag, noStreamFlag)
			if err != nil {
				return err
			}
			if verify {
				if deps.Verify == nil {
					return fmt.Errorf("%w: verification is not available", domain.ErrInvalidArgument)
				}
				stream = false // verification needs the whole answer; cannot verify a token stream
			}

			if stream {
				var raw strings.Builder
				w := cmd.OutOrStdout()
				ans, err := deps.Ask.SynthesizeStream(cmd.Context(), question, hits, attachments, func(tok string) {
					raw.WriteString(tok)
					_, _ = io.WriteString(w, tok)
				})
				if err != nil {
					return err
				}
				return finishStreamed(cmd, ans, hits, raw.String(), expand, false, nil)
			}

			ans, err := deps.Ask.Synthesize(cmd.Context(), question, hits, attachments)
			if err != nil {
				return err
			}

			citations := make([]citationView, len(ans.Citations))
			for i, c := range ans.Citations {
				citations[i] = citationView{ChunkID: string(c.ChunkID), Source: c.Source, Seq: c.Seq, Collection: c.Collection}
			}
			view := answerView{Text: ans.Text, Citations: citations, Grounded: ans.Grounded}
			md := answerMarkdown(ans)

			// --expand: append each cited chunk's full text, taken from the piped hits
			// (which carry it) — no collection lookup.
			if expand {
				byID := evidenceFromHits(hits)
				textByChunk := map[domain.ChunkID]string{}
				for _, c := range ans.Citations {
					if _, seen := textByChunk[c.ChunkID]; seen {
						continue
					}
					if t, ok := byID[c.ChunkID]; ok {
						textByChunk[c.ChunkID] = t
						view.Expansions = append(view.Expansions, chunkView{ChunkID: string(c.ChunkID), Seq: c.Seq, Text: t})
					}
				}
				md = expandedSources(ans, textByChunk)
			}

			// --verify: judge each claim against its cited piped chunks; --verify-strict
			// turns any unsupported claim into a non-zero exit (gateErr, code 5),
			// returned only after the report renders so CI sees what failed.
			var gateErr error
			if verify {
				claims, err := deps.Verify.VerifyWithEvidence(cmd.Context(), ans, evidenceFromHits(hits))
				if err != nil {
					return err
				}
				view.Verification = verificationViews(claims)
				sr := domain.SupportRate(claims)
				view.SupportRate = &sr
				md += "\n\n" + verificationMarkdown(claims, sr)
				if verifyStrict && !domain.AllSupported(claims) {
					gateErr = fmt.Errorf("synthesize: %w: %d/%d claims supported", app.ErrGateUnmet, supportedCount(claims), len(claims))
				}
			}

			if err := render(cmd, view, md); err != nil {
				return err
			}
			return gateErr
		},
	}
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "file to send to the model as an attachment (repeatable)")
	cmd.Flags().BoolVar(&verify, "verify", false, "check each answer sentence is entailed by the chunk(s) it cites (evidence is the piped hits); report per-claim verdicts and a support rate")
	cmd.Flags().BoolVar(&verifyStrict, "verify-strict", false, "as --verify, but exit non-zero (5) if any claim is unsupported (a CI faithfulness gate)")
	cmd.Flags().BoolVar(&expand, "expand", false, "append the full text of each cited chunk after the answer")
	cmd.Flags().BoolVar(&streamFlag, "stream", false, "stream the answer's tokens as they arrive (forced on; the default on an interactive terminal)")
	cmd.Flags().BoolVar(&noStreamFlag, "no-stream", false, "disable streaming; buffer and render the full answer (restores rich Markdown output)")
	return cmd
}

// evidenceFromHits maps each piped hit's chunk ID to its text, the evidence for
// --verify and --expand (the chunks are the grounding the caller supplied).
func evidenceFromHits(hits []domain.ChunkHit) map[domain.ChunkID]string {
	m := make(map[domain.ChunkID]string, len(hits))
	for _, h := range hits {
		m[h.Chunk.ID] = h.Chunk.Text
	}
	return m
}

// readHits decodes a JSON array of query hits (the shape `query --json` emits)
// into ChunkHits. Empty input means no hits. DocumentID is not carried on the
// wire and is not needed downstream — synthesis cites by chunk ID, seq, and
// source, all of which are present.
func readHits(r io.Reader) ([]domain.ChunkHit, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var views []hitView
	if err := json.Unmarshal(data, &views); err != nil {
		return nil, fmt.Errorf("%w: parse hits from stdin: %v", domain.ErrInvalidArgument, err)
	}
	hits := make([]domain.ChunkHit, len(views))
	for i, v := range views {
		hits[i] = domain.ChunkHit{
			Chunk:      domain.Chunk{ID: domain.ChunkID(v.ChunkID), Seq: v.Seq, Text: v.Text},
			Score:      v.Score,
			Source:     v.Source,
			Collection: v.Collection,
		}
	}
	return hits, nil
}
