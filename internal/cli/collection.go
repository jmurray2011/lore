package cli

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"

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
	var docURI string
	cmd := &cobra.Command{
		Use:   "rm <collection>",
		Short: "Remove a collection, or a single document with --doc",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: rm takes exactly one collection name", domain.ErrInvalidArgument)
			}
			collection := args[0]

			if docURI != "" {
				if err := deps.Remove.RemoveDocument(cmd.Context(), collection, docURI); err != nil {
					return err
				}
				view := rmView{Removed: "document", Collection: collection, Document: docURI}
				return render(cmd, view, fmt.Sprintf("Removed document `%s` from **%s**.", docURI, collection))
			}

			if err := deps.Remove.RemoveCollection(cmd.Context(), collection); err != nil {
				return err
			}
			return render(cmd, rmView{Removed: "collection", Collection: collection}, fmt.Sprintf("Removed collection **%s**.", collection))
		},
	}
	cmd.Flags().StringVar(&docURI, "doc", "", "remove only this document, by source URI")
	return cmd
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
