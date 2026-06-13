// Package cli is lore's driving adapter: it translates cobra commands into use
// case calls and renders results. stdout carries data (human text, or JSON with
// --json); logs and errors go to stderr (DESIGN.md). It holds no business logic
// and imports no storage/provider adapters — the composition root wires those
// into Deps.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Deps are the use cases the commands invoke, wired by the composition root.
type Deps struct {
	Catalog *app.Catalog
	Ingest  *app.Ingestor
	Sync    *app.Syncer
	Query   *app.Querier
	Ask     *app.Asker
	Remove  *app.Remover
}

// NewRootCommand builds the lore command tree, writing data to out and errors to
// errOut. Errors returned from Execute map to process exit codes via ExitCode.
func NewRootCommand(deps Deps, version string, out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "lore",
		Short:         "Fast, scriptable RAG and LLM operations over specific document sets",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().Bool("json", false, "emit machine-readable JSON to stdout")
	root.PersistentFlags().Bool("no-color", false, "disable ANSI color in human output")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %v", domain.ErrInvalidArgument, err)
	})
	root.AddCommand(
		newInitCmd(deps),
		newAddCmd(deps),
		newSyncCmd(deps),
		newLsCmd(deps),
		newStatusCmd(deps),
		newDocsCmd(deps),
		newQueryCmd(deps),
		newAskCmd(deps),
		newRmCmd(deps),
	)
	return root
}

// ExitCode maps a command error to a lore exit code (DESIGN.md): 0 ok,
// 1 runtime, 2 usage, 3 not found, 4 invariant violation.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, domain.ErrInvalidArgument):
		return 2
	case errors.Is(err, app.ErrNotFound):
		return 3
	case errors.Is(err, domain.ErrSpaceMismatch):
		return 4
	default:
		return 1
	}
}

// render writes v as indented JSON when --json is set; otherwise it treats the
// human string as Markdown — glamour-rendered for an interactive terminal, or
// emitted raw (clean for pipes, tests, and --no-color).
func render(cmd *cobra.Command, jsonValue any, markdown string) error {
	w := cmd.OutOrStdout()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(jsonValue)
	}
	if markdown == "" {
		return nil
	}
	if richEnabled(cmd) {
		if out, err := renderMarkdown(w, markdown); err == nil {
			_, err := io.WriteString(w, out)
			return err
		}
		// Fall through to raw Markdown if glamour fails for any reason.
	}
	_, err := fmt.Fprintln(w, markdown)
	return err
}

// Stable JSON output shapes.

type collectionView struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  string `json:"created_at"`
}

func viewCollection(c *domain.Collection) collectionView {
	return collectionView{
		Name:       c.Name,
		Model:      c.Space.Model,
		Dimensions: c.Space.Dimensions,
		CreatedAt:  c.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type hitView struct {
	ChunkID string  `json:"chunk_id"`
	Source  string  `json:"source"`
	Seq     int     `json:"seq"`
	Score   float64 `json:"score"`
	Text    string  `json:"text"`
}

type citationView struct {
	ChunkID string `json:"chunk_id"`
	Source  string `json:"source"`
	Seq     int    `json:"seq"`
}

type answerView struct {
	Text      string         `json:"text"`
	Citations []citationView `json:"citations"`
}

type docView struct {
	Source     string `json:"source"`
	Hash       string `json:"hash"`
	IngestedAt string `json:"ingested_at"`
}

type rmView struct {
	Removed    string `json:"removed"` // "collection" or "document"
	Collection string `json:"collection"`
	Document   string `json:"document,omitempty"`
}
