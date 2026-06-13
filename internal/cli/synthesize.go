package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/domain"
)

func newSynthesizeCmd(deps *Deps) *cobra.Command {
	var attach []string
	cmd := &cobra.Command{
		Use:   "synthesize <question>",
		Short: "Synthesize an answer from chunk hits read as JSON on stdin",
		Long: "Read query hits (the JSON array `lore query --json` emits) from stdin and " +
			"synthesize an answer, letting you interpose your own filtering, re-ranking, or " +
			"merging between retrieval and synthesis:\n\n" +
			"  lore query kb \"...\" --json | jq 'map(select(.score > 0.3))' | lore synthesize \"...\"\n\n" +
			"Unlike `ask`, it performs no retrieval and has no --strict: the hits you pipe in are " +
			"the grounding.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: synthesize takes a question (hits come from stdin)", domain.ErrInvalidArgument)
			}
			attachments, err := loadAttachments(attach)
			if err != nil {
				return err
			}
			hits, err := readHits(cmd.InOrStdin())
			if err != nil {
				return err
			}
			ans, err := deps.Ask.Synthesize(cmd.Context(), args[0], hits, attachments)
			if err != nil {
				return err
			}
			citations := make([]citationView, len(ans.Citations))
			for i, c := range ans.Citations {
				citations[i] = citationView{ChunkID: string(c.ChunkID), Source: c.Source, Seq: c.Seq}
			}
			view := answerView{Text: ans.Text, Citations: citations, Grounded: ans.Grounded}
			return render(cmd, view, answerMarkdown(ans))
		},
	}
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "file to send to the model as an attachment (repeatable)")
	return cmd
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
			Chunk:  domain.Chunk{ID: domain.ChunkID(v.ChunkID), Seq: v.Seq, Text: v.Text},
			Score:  v.Score,
			Source: v.Source,
		}
	}
	return hits, nil
}
