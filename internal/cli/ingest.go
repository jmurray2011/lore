package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

type ingestView struct {
	Added       int `json:"added"`
	Skipped     int `json:"skipped"`
	Unsupported int `json:"unsupported"`
	Chunks      int `json:"chunks"`
}

type syncView struct {
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Unsupported int      `json:"unsupported"`
	Chunks      int      `json:"chunks"`
	Pruned      int      `json:"pruned"`
	PrunedURIs  []string `json:"pruned_uris,omitempty"`
	DryRun      bool     `json:"dry_run,omitempty"`
}

// unsupportedClause renders ", N unsupported" only when there are any, so the
// common (zero) case stays quiet while a real data gap is surfaced inline.
func unsupportedClause(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", **%d** unsupported", n)
}

func newAddCmd(deps *Deps) *cobra.Command {
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
				total.Unsupported += sum.Unsupported
				total.Chunks += sum.Chunks
			}

			view := ingestView{Added: total.Added, Skipped: total.Skipped, Unsupported: total.Unsupported, Chunks: total.Chunks}
			human := fmt.Sprintf("Added **%d**, skipped **%d**%s — **%d** chunks.",
				total.Added, total.Skipped, unsupportedClause(total.Unsupported), total.Chunks)
			return render(cmd, view, human)
		},
	}
}

func newSyncCmd(deps *Deps) *cobra.Command {
	var prune, dryRun bool
	cmd := &cobra.Command{
		Use:   "sync <collection> [path]...",
		Short: "Re-ingest a collection's sources, optionally pruning deleted documents",
		Long: "Re-ingest the collection's sources (idempotent), re-embedding changed " +
			"documents. With no path, the sources remembered from prior add/sync runs are " +
			"replayed. With --prune, documents whose source files no longer exist are removed; " +
			"add --dry-run to preview that removal without changing anything.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("%w: sync takes a collection and optional paths", domain.ErrInvalidArgument)
			}
			sum, err := deps.Sync.Sync(cmd.Context(), args[0], args[1:], prune, dryRun)
			if err != nil {
				return err
			}
			view := syncView{
				Added: sum.Added, Skipped: sum.Skipped, Unsupported: sum.Unsupported,
				Chunks: sum.Chunks, Pruned: sum.Pruned, PrunedURIs: sum.PrunedURIs, DryRun: dryRun,
			}
			return render(cmd, view, syncMarkdown(sum, dryRun))
		},
	}
	cmd.Flags().BoolVar(&prune, "prune", false, "remove documents whose source file no longer exists")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --prune, list what would be removed without removing it")
	return cmd
}

// syncMarkdown renders a sync result: a dry run lists the documents prune would
// remove (full source URIs, so the preview is unambiguous); a real run reports
// the summary counts.
func syncMarkdown(sum app.SyncSummary, dryRun bool) string {
	if dryRun {
		if sum.Pruned == 0 {
			return "Dry run: nothing to prune."
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Dry run — would prune **%d** document(s):\n\n", sum.Pruned)
		for _, uri := range sum.PrunedURIs {
			fmt.Fprintf(&b, "- %s\n", uri)
		}
		return b.String()
	}
	return fmt.Sprintf("Added **%d**, skipped **%d**%s, pruned **%d** — **%d** chunks.",
		sum.Added, sum.Skipped, unsupportedClause(sum.Unsupported), sum.Pruned, sum.Chunks)
}
