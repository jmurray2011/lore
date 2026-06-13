package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

type ingestView struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
	Chunks  int `json:"chunks"`
}

type syncView struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
	Chunks  int `json:"chunks"`
	Pruned  int `json:"pruned"`
}

func newAddCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "add <collection> <path>...",
		Short: "Ingest files into a collection (idempotent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("%w: add takes <collection> and at least one path", domain.ErrInvalidArgument)
			}
			collection := args[0]

			var total app.IngestSummary
			for _, path := range args[1:] {
				sum, err := deps.Ingest.Ingest(cmd.Context(), collection, path)
				if err != nil {
					return err
				}
				total.Added += sum.Added
				total.Skipped += sum.Skipped
				total.Chunks += sum.Chunks
			}

			view := ingestView{Added: total.Added, Skipped: total.Skipped, Chunks: total.Chunks}
			human := fmt.Sprintf("Added **%d**, skipped **%d** — **%d** chunks.", total.Added, total.Skipped, total.Chunks)
			return render(cmd, view, human)
		},
	}
}

func newSyncCmd(deps Deps) *cobra.Command {
	var prune bool
	cmd := &cobra.Command{
		Use:   "sync <collection> [path]...",
		Short: "Re-ingest a collection's sources, optionally pruning deleted documents",
		Long: "Re-ingest the collection's sources (idempotent), re-embedding changed " +
			"documents. With no path, the sources remembered from prior add/sync runs are " +
			"replayed. With --prune, documents whose source files no longer exist are removed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("%w: sync takes a collection and optional paths", domain.ErrInvalidArgument)
			}
			sum, err := deps.Sync.Sync(cmd.Context(), args[0], args[1:], prune)
			if err != nil {
				return err
			}
			view := syncView{Added: sum.Added, Skipped: sum.Skipped, Chunks: sum.Chunks, Pruned: sum.Pruned}
			human := fmt.Sprintf("Added **%d**, skipped **%d**, pruned **%d** — **%d** chunks.", sum.Added, sum.Skipped, sum.Pruned, sum.Chunks)
			return render(cmd, view, human)
		},
	}
	cmd.Flags().BoolVar(&prune, "prune", false, "remove documents whose source file no longer exists")
	return cmd
}
