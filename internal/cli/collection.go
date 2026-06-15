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
				uri, err := resolveDocSelector(cmd, deps, collection, docURI)
				if err != nil {
					return err
				}
				if err := deps.Remove.RemoveDocument(cmd.Context(), collection, uri); err != nil {
					return err
				}
				view := rmView{Removed: "document", Collection: collection, Document: uri}
				return render(cmd, view, fmt.Sprintf("Removed document `%s` from **%s**.", uri, collection))
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
	var where []string
	cmd := &cobra.Command{
		Use:   "docs <collection>",
		Short: "List the documents ingested into a collection",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: docs takes exactly one collection name", domain.ErrInvalidArgument)
			}
			filter, err := domain.ParseWhere(where)
			if err != nil {
				return err
			}
			list, err := deps.Catalog.ListDocuments(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// Sort by source URI so output is stable regardless of backend.
			sort.Slice(list, func(i, j int) bool { return list[i].SourceURI < list[j].SourceURI })

			views := make([]docView, 0, len(list))
			rows := make([][]string, 0, len(list))
			for _, d := range list {
				if !filter.Match(d.Metadata) {
					continue
				}
				ingested := d.IngestedAt.UTC().Format(time.RFC3339)
				views = append(views, docView{Source: d.SourceURI, Hash: string(d.Hash), IngestedAt: ingested, Metadata: d.Metadata})
				rows = append(rows, []string{shortLabel(d.SourceURI), humanTime(ingested)})
			}
			var human string
			if len(rows) > 0 {
				human = mdTable([]string{"Source", "Ingested"}, rows) +
					fmt.Sprintf("\n*%d documents*\n", len(rows))
			}
			return render(cmd, views, human)
		},
	}
	cmd.Flags().StringArrayVar(&where, "where", nil, "list only documents whose metadata matches this predicate, e.g. 'author=alice' (repeatable; ANDed)")
	return cmd
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

// catByDoc prints every chunk of one document, headed by the document name. The
// selector may be a basename/glob/substring (resolved against the collection) or
// a full source URI.
func catByDoc(cmd *cobra.Command, deps *Deps, collection, selector string) error {
	uri, err := resolveDocSelector(cmd, deps, collection, selector)
	if err != nil {
		return err
	}
	chunks, err := deps.Catalog.DocumentChunks(cmd.Context(), collection, uri)
	if err != nil {
		return err
	}
	views, body := chunkViews(chunks)
	return render(cmd, views, fmt.Sprintf("## %s\n\n%s", shortLabel(uri), body))
}

// resolveDocSelector loads the collection's documents and resolves a --doc
// selector to one source URI. It is the shared front door for cat --doc and
// rm --doc, so both accept the basenames `lore docs` prints, not just full URIs.
func resolveDocSelector(cmd *cobra.Command, deps *Deps, collection, selector string) (string, error) {
	docs, err := deps.Catalog.ListDocuments(cmd.Context(), collection)
	if err != nil {
		return "", err
	}
	return resolveDocURI(docs, selector)
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
		views[i] = chunkView{ChunkID: string(c.ID), Seq: c.Seq, HeadingPath: c.HeadingPath, Text: c.Text}
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		if c.HeadingPath != "" {
			fmt.Fprintf(&b, "**chunk %d** — %s\n\n%s\n", c.Seq, c.HeadingPath, c.Text)
		} else {
			fmt.Fprintf(&b, "**chunk %d**\n\n%s\n", c.Seq, c.Text)
		}
	}
	return views, strings.TrimRight(b.String(), "\n")
}

type docRefView struct {
	Source string `json:"source"`
	Hash   string `json:"hash"`
}

type docChangeView struct {
	Source string `json:"source"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type diffView struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	Added   []docRefView    `json:"added"`
	Removed []docRefView    `json:"removed"`
	Changed []docChangeView `json:"changed"`
}

func newDiffCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "diff <from-collection> <to-collection>",
		Short: "Show the document-level difference between two collections (added/removed/changed by source)",
		Long: "Compare two collections by source URI and per-document content hash, reporting which " +
			"documents were added, removed, or changed. It compares what was ingested, not how it was " +
			"embedded, so collections pinned to different embedding spaces (e.g. a collection and a " +
			"re-imported snapshot) can still be diffed. Exit status is 0 whether or not they differ.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: diff takes exactly two collection names", domain.ErrInvalidArgument)
			}
			from, to := args[0], args[1]
			d, err := deps.Catalog.Diff(cmd.Context(), from, to)
			if err != nil {
				return err
			}
			return render(cmd, viewDiff(from, to, d), diffMarkdown(from, to, d))
		},
	}
}

// viewDiff maps a domain diff to its JSON view, keeping the slices non-nil so
// the output is `[]`, not `null`, for an empty bucket.
func viewDiff(from, to string, d app.CollectionDiff) diffView {
	v := diffView{From: from, To: to, Added: []docRefView{}, Removed: []docRefView{}, Changed: []docChangeView{}}
	for _, r := range d.Added {
		v.Added = append(v.Added, docRefView{Source: r.SourceURI, Hash: r.Hash})
	}
	for _, r := range d.Removed {
		v.Removed = append(v.Removed, docRefView{Source: r.SourceURI, Hash: r.Hash})
	}
	for _, ch := range d.Changed {
		v.Changed = append(v.Changed, docChangeView{Source: ch.SourceURI, From: ch.From, To: ch.To})
	}
	return v
}

// diffMarkdown renders a human diff: a one-line summary, then a section per
// non-empty bucket listing the documents by their short label.
func diffMarkdown(from, to string, d app.CollectionDiff) string {
	if d.Empty() {
		return fmt.Sprintf("Collections **%s** and **%s** hold the same documents.", from, to)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## diff: %s -> %s\n\n", from, to)
	fmt.Fprintf(&b, "**%d** added, **%d** removed, **%d** changed.\n", len(d.Added), len(d.Removed), len(d.Changed))
	section := func(title string, uris []string) {
		if len(uris) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n### %s\n\n", title)
		for _, uri := range uris {
			fmt.Fprintf(&b, "- %s\n", shortLabel(uri))
		}
	}
	added := make([]string, len(d.Added))
	for i, r := range d.Added {
		added[i] = r.SourceURI
	}
	removed := make([]string, len(d.Removed))
	for i, r := range d.Removed {
		removed[i] = r.SourceURI
	}
	changed := make([]string, len(d.Changed))
	for i, ch := range d.Changed {
		changed[i] = ch.SourceURI
	}
	section("Added", added)
	section("Removed", removed)
	section("Changed", changed)
	return b.String()
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
			chunker := chunkerLabel(coll)
			human := fmt.Sprintf("## %s\n\n- **model** — %s\n- **dimensions** — %d\n- **chunker** — %s\n- **documents** — %d\n- **created** — %s\n",
				coll.Name, coll.Space.Model, coll.Space.Dimensions, chunker, n,
				humanTime(coll.CreatedAt.UTC().Format(time.RFC3339)))
			return render(cmd, statusView{collectionView: viewCollection(coll), Documents: n, Chunker: chunker}, human)
		},
	}
}
