package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/domain"
)

func newQueryCmd(deps Deps) *cobra.Command {
	var k int
	cmd := &cobra.Command{
		Use:   "query <collection> <query>",
		Short: "Retrieve the most similar chunks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: query takes <collection> and a query string", domain.ErrInvalidArgument)
			}
			hits, err := deps.Query.Query(cmd.Context(), args[0], args[1], k)
			if err != nil {
				return err
			}
			views := make([]hitView, len(hits))
			var b strings.Builder
			for i, h := range hits {
				views[i] = hitView{ChunkID: string(h.Chunk.ID), Score: h.Score, Text: h.Chunk.Text}
				fmt.Fprintf(&b, "%.4f  %s\n    %s\n", h.Score, h.Chunk.ID, h.Chunk.Text)
			}
			return render(cmd, views, strings.TrimRight(b.String(), "\n"))
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to retrieve")
	return cmd
}

func newAskCmd(deps Deps) *cobra.Command {
	var k int
	cmd := &cobra.Command{
		Use:   "ask <collection> <question>",
		Short: "Answer a question grounded in the collection's chunks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: ask takes <collection> and a question", domain.ErrInvalidArgument)
			}
			ans, err := deps.Ask.Ask(cmd.Context(), args[0], args[1], k)
			if err != nil {
				return err
			}
			citations := make([]string, len(ans.Citations))
			for i, c := range ans.Citations {
				citations[i] = string(c)
			}
			human := ans.Text
			if len(citations) > 0 {
				human += "\n\nsources: " + strings.Join(citations, ", ")
			}
			return render(cmd, answerView{Text: ans.Text, Citations: citations}, human)
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to ground on")
	return cmd
}
