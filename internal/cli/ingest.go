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
			human := fmt.Sprintf("added %d, skipped %d (%d chunks)", total.Added, total.Skipped, total.Chunks)
			return render(cmd, view, human)
		},
	}
}
