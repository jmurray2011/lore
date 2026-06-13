package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/domain"
)

func newRerankCmd(deps *Deps) *cobra.Command {
	var topN int
	cmd := &cobra.Command{
		Use:   "rerank <query>",
		Short: "Rerank chunk hits read as JSON on stdin with a cross-encoder",
		Long: "Read query hits (the JSON array `lore query --json` emits) from stdin and reorder " +
			"them by cross-encoder relevance to <query> — the precision half of two-stage retrieval:\n\n" +
			"  lore query kb \"q\" -k 50 --json | lore rerank \"q\" -n 5 | lore synthesize \"q\"\n\n" +
			"Each hit gains a rerank_score (the original similarity score is kept). Without -n, all " +
			"hits are re-emitted reordered, so truncation stays composable with downstream tools.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: rerank takes a query (hits come from stdin)", domain.ErrInvalidArgument)
			}
			if deps.Rerank == nil {
				return errRerankUnconfigured()
			}
			hits, err := readHits(cmd.InOrStdin())
			if err != nil {
				return err
			}
			// Empty stdin → empty output, exit 0 (the use case makes no request).
			reranked, err := deps.Rerank.Rerank(cmd.Context(), args[0], hits, topN)
			if err != nil {
				return err
			}
			views, md := hitViews(reranked)
			return render(cmd, views, md)
		},
	}
	cmd.Flags().IntVarP(&topN, "top-n", "n", 0, "truncate to the top N hits after reranking (0 = all, reordered)")
	return cmd
}
