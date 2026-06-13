package cli

import (
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Human (non-JSON) output is built as Markdown and rendered for the terminal with
// glamour (glow's engine) when stdout is an interactive TTY; otherwise the raw
// Markdown is emitted, which stays clean for pipes, tests, and --no-color.

// richEnabled reports whether to glamour-render: an interactive terminal not
// suppressed by --json, --no-color, or the NO_COLOR convention.
func richEnabled(cmd *cobra.Command) bool {
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return false
	}
	if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return isTerminal(cmd.OutOrStdout())
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// renderMarkdown styles Markdown for the terminal via glamour, auto-detecting the
// light/dark theme and wrapping to the terminal width.
func renderMarkdown(w io.Writer, md string) (string, error) {
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(termWidth(w)))
	if err != nil {
		return "", err
	}
	return r.Render(md)
}

// termWidth is the terminal column count, or a sensible default off-terminal.
func termWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
			return cols
		}
	}
	return 100
}

// citeRefRE matches bracketed citations like [<chunkID>] (possibly several
// comma-separated IDs) in answer prose.
var citeRefRE = regexp.MustCompile(`\[([^\[\]]+)\]`)

// answerMarkdown formats a grounded answer as Markdown: the model's inline
// [chunkID] references become compact numbered [n] markers (numbered by first
// appearance, deduped), followed by a "## Sources" ordered list of short,
// readable labels. Citations the prose never referenced inline (e.g. from
// structured-output JSON) are appended. Unknown bracketed tokens are untouched.
func answerMarkdown(ans app.Answer) string {
	text, order := numberedAnswer(ans)
	if len(order) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\n## Sources\n\n")
	for i, c := range order {
		fmt.Fprintf(&b, "%d. %s\n", i+1, citationLabel(c))
	}
	return b.String()
}

// numberedAnswer rewrites the model's inline [chunkID] references to compact
// numbered [n] markers (numbered by first appearance, deduped) and returns the
// rewritten prose plus the cited citations in that display order (citations
// never referenced inline — e.g. from structured output — are appended). It is
// the shared basis for both the plain "## Sources" list and --expand, so their
// ordinals always match the answer's.
func numberedAnswer(ans app.Answer) (string, []domain.Citation) {
	byID := make(map[domain.ChunkID]domain.Citation, len(ans.Citations))
	for _, c := range ans.Citations {
		byID[c.ChunkID] = c
	}

	num := make(map[domain.ChunkID]int, len(ans.Citations))
	var order []domain.Citation
	assign := func(c domain.Citation) int {
		if n, ok := num[c.ChunkID]; ok {
			return n
		}
		order = append(order, c)
		num[c.ChunkID] = len(order)
		return len(order)
	}

	text := citeRefRE.ReplaceAllStringFunc(ans.Text, func(m string) string {
		var b strings.Builder
		matched := false
		for _, part := range strings.Split(m[1:len(m)-1], ",") {
			if c, ok := byID[domain.ChunkID(strings.TrimSpace(part))]; ok {
				matched = true
				fmt.Fprintf(&b, "[%d]", assign(c))
			}
		}
		if matched {
			return b.String()
		}
		return m
	})

	for _, c := range ans.Citations { // cited but not referenced inline
		assign(c)
	}
	return text, order
}

// expandedSources renders the answer (with inline [n] markers) followed by a
// Sources: block that includes each cited chunk's full text, numbered with the
// same ordinals the answer uses. textByChunk supplies the chunk text fetched via
// the slice-1 GetChunksByIDs. With nothing cited, just the answer is returned.
func expandedSources(ans app.Answer, textByChunk map[domain.ChunkID]string) string {
	text, order := numberedAnswer(ans)
	if len(order) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\nSources:\n")
	for i, c := range order {
		fmt.Fprintf(&b, "\n[%d] (%s) — %s#%d\n\n%s\n", i+1, shortLabel(c.Source), c.Source, c.Seq, textByChunk[c.ChunkID])
	}
	return strings.TrimRight(b.String(), "\n")
}

// citedChunkIDs returns the cited chunk IDs in the answer's display order — used
// to fetch their full text for --expand without re-parsing the rendered answer.
func citedChunkIDs(ans app.Answer) []string {
	_, order := numberedAnswer(ans)
	ids := make([]string, len(order))
	for i, c := range order {
		ids[i] = string(c.ChunkID)
	}
	return ids
}

// citedSet returns the set of chunk IDs the answer cited, for cross-referencing
// against the retrieved hits in --explain.
func citedSet(ans app.Answer) map[domain.ChunkID]bool {
	cited := make(map[domain.ChunkID]bool, len(ans.Citations))
	for _, c := range ans.Citations {
		cited[c.ChunkID] = true
	}
	return cited
}

// buildExplain assembles the --explain diagnostic from the returned hits and the
// runner-up score just outside them. A non-nil cited map (ask only — it has an
// answer) annotates each hit with whether the answer cited it; for query it is
// nil and Cited is omitted.
func buildExplain(hits []domain.ChunkHit, nextScore *float64, cited map[domain.ChunkID]bool) explainView {
	ev := explainView{Returned: make([]explainHit, len(hits)), NextScore: nextScore}
	var sum, min, max float64
	for i, h := range hits {
		eh := explainHit{Score: h.Score, Source: h.Source, Seq: h.Chunk.Seq}
		if cited != nil {
			c := cited[h.Chunk.ID]
			eh.Cited = &c
		}
		ev.Returned[i] = eh
		sum += h.Score
		if i == 0 || h.Score < min {
			min = h.Score
		}
		if i == 0 || h.Score > max {
			max = h.Score
		}
	}
	if len(hits) > 0 {
		ev.Stats = explainStats{Min: min, Max: max, Mean: sum / float64(len(hits))}
	}
	return ev
}

// explainText renders the diagnostic as a plain-text block for stderr (never
// stdout, so a piped answer or hit array stays clean). Read it as: scores low
// everywhere → retrieval is starving (fix chunking/embeddings/filters); a
// high-scoring chunk left uncited → the model ignored context (a synthesis
// problem).
func explainText(ev explainView) string {
	var b strings.Builder
	next := "none"
	if ev.NextScore != nil {
		next = fmt.Sprintf("%.4f", *ev.NextScore)
	}
	fmt.Fprintf(&b, "retrieval: %d returned, scores %.4f–%.4f (mean %.4f); next candidate %s\n",
		len(ev.Returned), ev.Stats.Min, ev.Stats.Max, ev.Stats.Mean, next)
	for i, h := range ev.Returned {
		label := fmt.Sprintf("%s#%d", shortLabel(h.Source), h.Seq)
		switch {
		case h.Cited == nil:
			fmt.Fprintf(&b, "  [%d] %.4f  %s\n", i+1, h.Score, label)
		case *h.Cited:
			fmt.Fprintf(&b, "  [%d] %.4f  cited    %s\n", i+1, h.Score, label)
		default:
			fmt.Fprintf(&b, "  [%d] %.4f  uncited  %s\n", i+1, h.Score, label)
		}
	}
	return b.String()
}

// citationLabel is the short, human form of a citation: "file.docx · chunk 3".
func citationLabel(c domain.Citation) string {
	return fmt.Sprintf("%s · chunk %d", shortLabel(c.Source), c.Seq)
}

// shortLabel reduces a source URI to its basename for human display; the full
// URI remains available via --json.
func shortLabel(uri string) string {
	if i := strings.Index(uri, "://"); i >= 0 {
		uri = uri[i+3:]
	}
	uri = strings.TrimRight(uri, "/")
	if uri == "" {
		return ""
	}
	return path.Base(uri)
}

// mdTable renders header + rows as a GitHub-flavored Markdown table (pipes in
// cells escaped). glamour draws it with a styled header and borders.
func mdTable(header []string, rows [][]string) string {
	var b strings.Builder
	writeRow := func(cells []string) {
		b.WriteByte('|')
		for _, c := range cells {
			b.WriteString(" " + strings.ReplaceAll(c, "|", "\\|") + " |")
		}
		b.WriteByte('\n')
	}
	writeRow(header)
	b.WriteByte('|')
	for range header {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, r := range rows {
		writeRow(r)
	}
	return b.String()
}

// humanTime renders an RFC3339 timestamp as "2006-01-02 15:04" (UTC), falling
// back to the raw value if it doesn't parse.
func humanTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format("2006-01-02 15:04")
}
