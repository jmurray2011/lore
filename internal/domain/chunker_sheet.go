package domain

import "strings"

// SheetHeadingPrefix introduces a sheet boundary in extracted tabular text. An
// extractor that can name its tables (the xlsx extractor, from the workbook's
// tab names) emits one such line before each table's rows; SheetChunker splits
// on it and repeats it on every chunk. It lives in domain rather than beside the
// extractor because it is the contract between them, and both sides must agree
// on it — the tabular analogue of markdown's "#".
const SheetHeadingPrefix = "# Sheet: "

// SheetChunker splits tabular text — one record per line — into token-sized
// chunks that stay readable on their own. A table's header row is the only thing
// naming its columns, and a sheet's tab name the only thing naming the table, so
// both are repeated at the top of every chunk cut from that table. Without this
// a chunk from the middle of a workbook retrieves as bare cells whose columns
// the reader (and the model) cannot recover.
//
// Unlike MarkdownChunker's context prefix, the repetition goes into the stored
// Text, not just EmbedText: the header is genuinely absent from the interior of
// a table, so a reader of the cited chunk needs it as much as the embedding
// does.
//
// Rows are never split across chunks and never duplicated between them; the
// header is the shared context instead of an overlap window. The first line of
// each table is taken to be its header, which is wrong for a workbook with
// banner rows above the real header — a limitation, not a heuristic to grow.
//
// Deterministic and pure. Construct with NewSheetChunker; the zero value is not
// usable.
type SheetChunker struct {
	tokenPacker
}

// compile-time interface check
var _ Chunker = SheetChunker{}

// NewSheetChunker returns a SheetChunker targeting size tokens per chunk. The
// overlap applies only when a single row is itself too large to fit and has to
// be hard-split. It requires size > 0, 0 <= overlap < size, and a non-nil
// counter.
func NewSheetChunker(size, overlap int, countTokens func(string) int) (SheetChunker, error) {
	p, err := newTokenPacker("sheet", size, overlap, countTokens)
	if err != nil {
		return SheetChunker{}, err
	}
	return SheetChunker{tokenPacker: p}, nil
}

// Chunk splits the document into per-table, header-carrying chunks.
func (c SheetChunker) Chunk(doc ParsedDoc) ([]ChunkResult, error) {
	var results []ChunkResult
	for _, t := range splitTables(doc.Text) {
		results = append(results, c.chunkTable(t)...)
	}
	return results, nil
}

// sheetTable is one table: its sheet name (empty when the source could not name
// it, e.g. a csv) and its rows in order, header first.
type sheetTable struct {
	name string
	rows []string
}

// chunkTable emits the chunks for one table, each led by the sheet name and
// header row.
func (c SheetChunker) chunkTable(t sheetTable) []ChunkResult {
	if len(t.rows) == 0 {
		return nil
	}
	var lead []string
	if t.name != "" {
		lead = append(lead, SheetHeadingPrefix+t.name)
	}
	header, body := t.rows[0], t.rows[1:]
	lead = append(lead, header)
	prefix := strings.Join(lead, "\n")

	emit := func(rows []string) ChunkResult {
		text := prefix
		if len(rows) > 0 {
			text += "\n" + strings.Join(rows, "\n")
		}
		return ChunkResult{Text: text, HeadingPath: t.name}
	}

	if len(body) == 0 {
		return []ChunkResult{emit(nil)}
	}

	// Every chunk pays for the repeated lead, so the rows only get what is left.
	budget := c.size - c.countTokens(prefix+"\n")
	if budget <= 0 {
		// The header alone fills a chunk; repeating it would leave no room for
		// data. Degrade to plain packing rather than emit context-only chunks.
		var out []ChunkResult
		for _, text := range c.emitTexts(strings.Join(t.rows, "\n")) {
			out = append(out, ChunkResult{Text: text, HeadingPath: t.name})
		}
		return out
	}
	rowPacker := c.withSize(budget)

	var (
		out []ChunkResult
		buf []string
		n   int
	)
	flush := func() {
		if len(buf) > 0 {
			out = append(out, emit(buf))
			buf, n = nil, 0
		}
	}
	for _, row := range body {
		size := c.countTokens(row)
		if size > budget {
			// A single row wider than the budget: hard-split it, each piece still
			// carrying the lead so the columns stay recoverable.
			flush()
			for _, piece := range rowPacker.wordWindows(row) {
				out = append(out, emit([]string{piece}))
			}
			continue
		}
		if n+size > budget {
			flush()
		}
		buf = append(buf, row)
		n += size
	}
	flush()
	return out
}

// withSize returns a copy of the packer sized to n tokens, for packing content
// that has to share a chunk with a fixed prefix. Overlap is clamped so it stays
// below the reduced size.
func (p tokenPacker) withSize(n int) tokenPacker {
	overlap := p.overlap
	if overlap >= n {
		overlap = n - 1
	}
	return tokenPacker{size: n, overlap: overlap, countTokens: p.countTokens}
}

// splitTables divides tabular text into tables at SheetHeadingPrefix lines.
// Content before the first marker (or in a document with no markers at all, such
// as a csv) is one unnamed table. Blank lines are dropped: they separate sheets
// in the extractor's output and carry no record of their own.
func splitTables(text string) []sheetTable {
	var (
		tables []sheetTable
		cur    sheetTable
		opened bool
	)
	flush := func() {
		if len(cur.rows) > 0 {
			tables = append(tables, cur)
		}
		cur = sheetTable{}
	}
	for _, line := range strings.Split(text, "\n") {
		if name, ok := strings.CutPrefix(line, SheetHeadingPrefix); ok {
			flush()
			cur.name = strings.TrimSpace(name)
			opened = true
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		cur.rows = append(cur.rows, strings.TrimRight(line, " \t"))
		opened = true
	}
	if opened {
		flush()
	}
	return tables
}
