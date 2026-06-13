package cli

import (
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// style renders human (non-JSON) output. When color is false every helper is a
// no-op passthrough, so the same code path produces plain text for pipes, tests,
// and --no-color. ANSI is only ever emitted to an interactive terminal.
type style struct{ color bool }

// styleForCmd derives a style from the command's output: color is on only for an
// interactive terminal, with --json and --no-color and the NO_COLOR convention
// all forcing it off.
func styleForCmd(cmd *cobra.Command) *style {
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return &style{}
	}
	if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
		return &style{}
	}
	return &style{color: isTerminal(cmd.OutOrStdout())}
}

// isTerminal reports whether w is an interactive terminal, honoring NO_COLOR.
// It uses the stdlib char-device check (no x/term dependency): good enough to
// decide coloring, and false for the *bytes.Buffer used in tests.
func isTerminal(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (s *style) wrap(code, text string) string {
	if !s.color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s *style) bold(t string) string  { return s.wrap("1", t) }
func (s *style) faint(t string) string { return s.wrap("2", t) }
func (s *style) cyan(t string) string  { return s.wrap("36", t) }
func (s *style) green(t string) string { return s.wrap("32", t) }

// citeRefRE matches bracketed citations like [<chunkID>] (possibly several
// comma-separated IDs) in answer prose.
var citeRefRE = regexp.MustCompile(`\[([^\[\]]+)\]`)

// answer formats a grounded answer: the model's inline [chunkID] references
// become compact numbered [n] markers (numbered by first appearance, deduped),
// followed by a numbered Sources list of short, readable labels. Citations the
// prose never referenced inline (e.g. from structured-output JSON) are appended
// to the list. Unknown bracketed tokens are left untouched.
func (s *style) answer(ans app.Answer) string {
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
				b.WriteString(s.cyan(fmt.Sprintf("[%d]", assign(c))))
			}
		}
		if matched {
			return b.String()
		}
		return m
	})

	for _, c := range ans.Citations { // any cited but not referenced inline
		assign(c)
	}

	if len(order) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\n")
	b.WriteString(s.bold("Sources"))
	for i, c := range order {
		fmt.Fprintf(&b, "\n  %s %s", s.cyan(fmt.Sprintf("[%d]", i+1)), s.faint(citationLabel(c)))
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

// table renders header + rows as aligned columns. Color, when on, bolds only the
// whole header line — never individual cells, whose ANSI bytes would defeat the
// tabwriter's width calculation and misalign the columns.
func (s *style) table(header []string, rows [][]string) string {
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) > 0 {
		lines[0] = s.bold(lines[0])
	}
	return strings.Join(lines, "\n")
}

// humanTime renders an RFC3339 timestamp as "2006-01-02 15:04" (local-agnostic,
// UTC), falling back to the raw value if it doesn't parse.
func humanTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format("2006-01-02 15:04")
}
