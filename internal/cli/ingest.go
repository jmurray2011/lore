package cli

import (
	"fmt"
	"io"
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
	var (
		stdin bool
		name  string
		ctype string
		meta  []string
	)
	cmd := &cobra.Command{
		Use:   "add <collection> <path>...  |  add <collection> --stdin",
		Short: "Ingest files (or piped stdin content) into a collection (idempotent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("%w: add takes a collection and at least one path (or --stdin)", domain.ErrInvalidArgument)
			}
			collection, paths := args[0], args[1:]

			md, err := parseMetaPairs(meta)
			if err != nil {
				return err
			}

			if stdin {
				if len(paths) > 0 {
					return fmt.Errorf("%w: --stdin takes no path arguments", domain.ErrInvalidArgument)
				}
				content, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				sum, err := deps.Ingest.IngestContent(cmd.Context(), collection, name, ctype, content, app.WithMeta(md))
				if err != nil {
					return err
				}
				return renderIngest(cmd, sum)
			}

			if len(paths) == 0 {
				return fmt.Errorf("%w: add takes at least one path (or --stdin)", domain.ErrInvalidArgument)
			}
			var total app.IngestSummary
			for _, path := range paths {
				sum, err := deps.Ingest.Ingest(cmd.Context(), collection, path, app.WithMeta(md))
				if err != nil {
					return err
				}
				total.Added += sum.Added
				total.Skipped += sum.Skipped
				total.Unsupported += sum.Unsupported
				total.Chunks += sum.Chunks
			}
			return renderIngest(cmd, total)
		},
	}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read one document's content from stdin instead of walking paths")
	cmd.Flags().StringVar(&name, "name", "stdin", "source name/URI to record for --stdin content")
	cmd.Flags().StringVar(&ctype, "type", "text/markdown", "content type of --stdin content")
	cmd.Flags().StringArrayVar(&meta, "meta", nil, "attach metadata key=value to ingested documents (repeatable); filter later with --where")
	return cmd
}

// parseMetaPairs parses repeatable --meta key=value flags into Metadata. A pair
// without '=' or with an empty key is a usage error. Whitespace around the key and
// value is trimmed.
func parseMetaPairs(pairs []string) (domain.Metadata, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	md := domain.Metadata{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%w: --meta %q must be key=value", domain.ErrInvalidArgument, p)
		}
		md[k] = strings.TrimSpace(v)
	}
	return md, nil
}

// renderIngest renders an ingestion summary (shared by path and stdin add).
func renderIngest(cmd *cobra.Command, sum app.IngestSummary) error {
	view := ingestView{Added: sum.Added, Skipped: sum.Skipped, Unsupported: sum.Unsupported, Chunks: sum.Chunks}
	human := fmt.Sprintf("Added **%d**, skipped **%d**%s — **%d** chunks.",
		sum.Added, sum.Skipped, unsupportedClause(sum.Unsupported), sum.Chunks)
	return render(cmd, view, human)
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
