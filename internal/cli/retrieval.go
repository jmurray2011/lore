package cli

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
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
				views[i] = hitView{ChunkID: string(h.Chunk.ID), Source: h.Source, Seq: h.Chunk.Seq, Score: h.Score, Text: h.Chunk.Text}
				if i > 0 {
					b.WriteString("\n---\n\n")
				}
				fmt.Fprintf(&b, "**[%d]**  %s  ·  `%.4f`\n\n%s\n", i+1, hitLabel(h), h.Score, h.Chunk.Text)
			}
			return render(cmd, views, strings.TrimRight(b.String(), "\n"))
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to retrieve")
	return cmd
}

func newAskCmd(deps Deps) *cobra.Command {
	var (
		k      int
		attach []string
	)
	cmd := &cobra.Command{
		Use:   "ask <collection> <question>",
		Short: "Answer a question grounded in the collection's chunks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: ask takes <collection> and a question", domain.ErrInvalidArgument)
			}
			attachments, err := loadAttachments(attach)
			if err != nil {
				return err
			}
			ans, err := deps.Ask.Ask(cmd.Context(), args[0], args[1], k, attachments)
			if err != nil {
				return err
			}
			citations := make([]citationView, len(ans.Citations))
			for i, c := range ans.Citations {
				citations[i] = citationView{ChunkID: string(c.ChunkID), Source: c.Source, Seq: c.Seq}
			}
			return render(cmd, answerView{Text: ans.Text, Citations: citations}, answerMarkdown(ans))
		},
	}
	cmd.Flags().IntVarP(&k, "top-k", "k", 8, "number of chunks to ground on (0 to ground on attachments only)")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "file to send to the model as an attachment (repeatable)")
	return cmd
}

// hitLabel renders a hit's provenance for human output: "file.docx · chunk 3"
// when the source is known, falling back to the opaque chunk ID otherwise.
func hitLabel(h domain.ChunkHit) string {
	if h.Source == "" {
		return string(h.Chunk.ID)
	}
	return fmt.Sprintf("%s · chunk %d", shortLabel(h.Source), h.Chunk.Seq)
}

// loadAttachments reads each path into an Attachment, detecting its media type
// from the file extension. An undetectable extension is a usage error so the
// caller learns the file won't be understood rather than silently dropping it.
func loadAttachments(paths []string) ([]domain.Attachment, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	attachments := make([]domain.Attachment, 0, len(paths))
	for _, path := range paths {
		mediaType, _, _ := strings.Cut(mime.TypeByExtension(filepath.Ext(path)), ";")
		if mediaType == "" {
			return nil, fmt.Errorf("%w: cannot determine media type of %q from its extension", domain.ErrInvalidArgument, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", path, err)
		}
		a, err := domain.NewAttachment(mediaType, filepath.Base(path), data)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, nil
}
