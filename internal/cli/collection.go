package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func newInitCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "init <collection>",
		Short: "Create a collection, pinned to the configured embedding space",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: init takes exactly one collection name", domain.ErrInvalidArgument)
			}
			coll, err := deps.Catalog.Init(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return render(cmd, viewCollection(coll), fmt.Sprintf("Created collection **%s** (%s).", coll.Name, coll.Space))
		},
	}
}

func newLsCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List collections",
		RunE: func(cmd *cobra.Command, _ []string) error {
			colls, err := deps.Catalog.List(cmd.Context())
			if err != nil {
				return err
			}
			views := make([]collectionView, len(colls))
			rows := make([][]string, len(colls))
			for i, c := range colls {
				views[i] = viewCollection(c)
				rows[i] = []string{c.Name, c.Space.Model, strconv.Itoa(c.Space.Dimensions)}
			}
			var human string
			if len(rows) > 0 {
				human = mdTable([]string{"Name", "Model", "Dims"}, rows)
			}
			return render(cmd, views, human)
		},
	}
}

func newRmCmd(deps *Deps) *cobra.Command {
	var (
		docURI   string
		chunkIDs []string
	)
	cmd := &cobra.Command{
		Use:   "rm <collection> [--doc <uri> | --chunk <id>...]",
		Short: "Remove a collection, a single document (--doc), or specific chunks (--chunk)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: rm takes exactly one collection name", domain.ErrInvalidArgument)
			}
			collection := args[0]

			switch {
			case docURI != "" && len(chunkIDs) > 0:
				return fmt.Errorf("%w: --doc and --chunk are mutually exclusive", domain.ErrInvalidArgument)
			case len(chunkIDs) > 0:
				return rmByChunk(cmd, deps, collection, chunkIDs)
			case docURI != "":
				if err := deps.Remove.RemoveDocument(cmd.Context(), collection, docURI); err != nil {
					return err
				}
				view := rmView{Removed: "document", Collection: collection, Document: docURI}
				return render(cmd, view, fmt.Sprintf("Removed document `%s` from **%s**.", docURI, collection))
			default:
				if err := deps.Remove.RemoveCollection(cmd.Context(), collection); err != nil {
					return err
				}
				return render(cmd, rmView{Removed: "collection", Collection: collection}, fmt.Sprintf("Removed collection **%s**.", collection))
			}
		},
	}
	cmd.Flags().StringVar(&docURI, "doc", "", "remove only this document, by source URI")
	cmd.Flags().StringArrayVar(&chunkIDs, "chunk", nil, "remove only these chunks, by ID (repeatable)")
	return cmd
}

// rmByChunk deletes specific chunks by ID — sub-document redaction (the chunks'
// document survives). Mirrors cat --chunk's input handling: a malformed ID is a
// usage error (exit 2); any requested-but-missing ID is warned on stderr while
// the found chunks are still removed, and the command exits 3. Note: removing a
// chunk drops it from the index only — re-ingesting its source document restores
// it. For permanent redaction, also remove or redact the source (or use rm --doc).
func rmByChunk(cmd *cobra.Command, deps *Deps, collection string, ids []string) error {
	want := make([]domain.ChunkID, len(ids))
	for i, id := range ids {
		if !domain.ChunkID(id).Valid() {
			return fmt.Errorf("%w: malformed chunk ID %q", domain.ErrInvalidArgument, id)
		}
		want[i] = domain.ChunkID(id)
	}

	removed, err := deps.Remove.RemoveChunks(cmd.Context(), collection, want)
	if err != nil {
		return err
	}

	removedSet := make(map[string]bool, len(removed))
	removedStrs := make([]string, len(removed))
	for i, id := range removed {
		removedSet[string(id)] = true
		removedStrs[i] = string(id)
	}
	var missing int
	for _, id := range ids {
		if !removedSet[id] {
			missing++
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "lore: warning: chunk %q not found in %q\n", id, collection)
		}
	}

	view := rmView{Removed: "chunks", Collection: collection, ChunkIDs: removedStrs}
	if err := render(cmd, view, fmt.Sprintf("Removed %d chunk(s) from **%s**.", len(removed), collection)); err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("%w: %d of %d requested chunks", app.ErrNotFound, missing, len(ids))
	}
	return nil
}

func newDocsCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "docs <collection>",
		Short: "List the documents ingested into a collection",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: docs takes exactly one collection name", domain.ErrInvalidArgument)
			}
			list, err := deps.Catalog.ListDocuments(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// Sort by source URI so output is stable regardless of backend.
			sort.Slice(list, func(i, j int) bool { return list[i].SourceURI < list[j].SourceURI })

			views := make([]docView, len(list))
			rows := make([][]string, len(list))
			for i, d := range list {
				ingested := d.IngestedAt.UTC().Format(time.RFC3339)
				views[i] = docView{Source: d.SourceURI, Hash: string(d.Hash), IngestedAt: ingested}
				rows[i] = []string{shortLabel(d.SourceURI), humanTime(ingested)}
			}
			var human string
			if len(rows) > 0 {
				human = mdTable([]string{"Source", "Ingested"}, rows) +
					fmt.Sprintf("\n*%d documents*\n", len(rows))
			}
			return render(cmd, views, human)
		},
	}
}

func newCatCmd(deps *Deps) *cobra.Command {
	var (
		docURI   string
		chunkIDs []string
	)
	cmd := &cobra.Command{
		Use:   "cat <collection> (--doc <uri> | --chunk <id>...)",
		Short: "Print stored chunks (the extracted, chunked text as indexed), by document or by chunk ID",
		Long: "Print stored chunks as they were indexed. Scope with --doc <source-uri> for a whole " +
			"document, or --chunk <id> (repeatable) for specific chunks by ID — useful for auditing the " +
			"chunks an answer cited. The two selectors are mutually exclusive.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: cat takes exactly one collection name", domain.ErrInvalidArgument)
			}
			collection := args[0]
			switch {
			case docURI != "" && len(chunkIDs) > 0:
				return fmt.Errorf("%w: --doc and --chunk are mutually exclusive", domain.ErrInvalidArgument)
			case len(chunkIDs) > 0:
				return catByChunk(cmd, deps, collection, chunkIDs)
			case docURI != "":
				return catByDoc(cmd, deps, collection, docURI)
			default:
				return fmt.Errorf("%w: cat requires --doc <source-uri> or --chunk <id>", domain.ErrInvalidArgument)
			}
		},
	}
	cmd.Flags().StringVar(&docURI, "doc", "", "source URI of the document to print")
	cmd.Flags().StringArrayVar(&chunkIDs, "chunk", nil, "chunk ID to print (repeatable)")
	return cmd
}

// catByDoc prints every chunk of one document, headed by the document name.
func catByDoc(cmd *cobra.Command, deps *Deps, collection, docURI string) error {
	chunks, err := deps.Catalog.DocumentChunks(cmd.Context(), collection, docURI)
	if err != nil {
		return err
	}
	views, body := chunkViews(chunks)
	return render(cmd, views, fmt.Sprintf("## %s\n\n%s", shortLabel(docURI), body))
}

// catByChunk prints specific chunks by ID. Malformed IDs are a usage error (exit
// 2); any requested-but-missing ID is warned on stderr while the found chunks
// still print to stdout, and the command exits 3 (not found).
func catByChunk(cmd *cobra.Command, deps *Deps, collection string, ids []string) error {
	for _, id := range ids {
		if !domain.ChunkID(id).Valid() {
			return fmt.Errorf("%w: malformed chunk ID %q", domain.ErrInvalidArgument, id)
		}
	}
	chunks, err := deps.Catalog.ChunksByIDs(cmd.Context(), collection, ids)
	if err != nil {
		return err
	}

	found := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		found[string(c.ID)] = true
	}
	var missing int
	for _, id := range ids {
		if !found[id] {
			missing++
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "lore: warning: chunk %q not found in %q\n", id, collection)
		}
	}

	views, body := chunkViews(chunks)
	if err := render(cmd, views, body); err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("%w: %d of %d requested chunks", app.ErrNotFound, missing, len(ids))
	}
	return nil
}

// chunkViews builds the JSON views and the shared human per-chunk Markdown body
// (one "**chunk N**" block per chunk, separated by ---) used by every cat path.
func chunkViews(chunks []domain.Chunk) ([]chunkView, string) {
	views := make([]chunkView, len(chunks))
	var b strings.Builder
	for i, c := range chunks {
		views[i] = chunkView{ChunkID: string(c.ID), Seq: c.Seq, Text: c.Text}
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&b, "**chunk %d**\n\n%s\n", c.Seq, c.Text)
	}
	return views, strings.TrimRight(b.String(), "\n")
}

func newStatusCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status <collection>",
		Short: "Show a collection's details",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: status takes exactly one collection name", domain.ErrInvalidArgument)
			}
			coll, err := deps.Catalog.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			docs, err := deps.Catalog.ListDocuments(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			n := len(docs)
			human := fmt.Sprintf("## %s\n\n- **model** — %s\n- **dimensions** — %d\n- **documents** — %d\n- **created** — %s\n",
				coll.Name, coll.Space.Model, coll.Space.Dimensions, n,
				humanTime(coll.CreatedAt.UTC().Format(time.RFC3339)))
			return render(cmd, statusView{collectionView: viewCollection(coll), Documents: n}, human)
		},
	}
}
