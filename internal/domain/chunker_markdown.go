package domain

import "strings"

// StructureChunkerVersion is the algorithm version of the structure-aware
// strategy (markdown sections + token sizing, plus the plain-text and tabular
// strategies that share its sizing), recorded in a ChunkerSpec. Bump it when any
// structure chunker's output could change for the same input.
//
// v2 added SheetChunker: tabular formats gained sheet names and repeated header
// rows, so their chunk text changed. The spec is collection-wide, so this
// invalidates collections that hold no spreadsheets either — they stay
// queryable, but refuse further ingest until rebuilt.
const StructureChunkerVersion = 2

// MarkdownChunker splits markdown into token-sized chunks that respect the
// document's heading hierarchy: each chunk is a section (the content under a
// heading), carrying the full heading path as metadata. It never breaks inside a
// fenced code block; oversized sections split at paragraph (then sentence, then
// word) boundaries, and undersized adjacent sections merge up to the target
// size. Deterministic and pure.
//
// Construct with NewMarkdownChunker; the zero value is not usable.
type MarkdownChunker struct {
	tokenPacker
	contextPrefix bool
}

// compile-time interface check
var _ Chunker = MarkdownChunker{}

// NewMarkdownChunker returns a MarkdownChunker targeting size tokens per chunk
// with overlap tokens shared between size-driven sub-splits. When contextPrefix
// is true, each chunk's embedded text is prefixed with its heading path so the
// embedding captures the chunk's place in the document (the stored text is
// unchanged). It requires size > 0, 0 <= overlap < size, and a non-nil counter.
func NewMarkdownChunker(size, overlap int, contextPrefix bool, countTokens func(string) int) (MarkdownChunker, error) {
	p, err := newTokenPacker("markdown", size, overlap, countTokens)
	if err != nil {
		return MarkdownChunker{}, err
	}
	return MarkdownChunker{tokenPacker: p, contextPrefix: contextPrefix}, nil
}

// Chunk splits the document into heading-aware, token-sized chunks.
func (c MarkdownChunker) Chunk(doc ParsedDoc) ([]ChunkResult, error) {
	var results []ChunkResult
	for _, g := range c.merge(splitSections(doc.Text)) {
		path := strings.Join(g.path, " > ")
		for _, text := range c.emitTexts(g.text) {
			r := ChunkResult{Text: text, HeadingPath: path}
			// Prepend the heading path to the embedded text only (the stored Text
			// stays original, so citations and inspection show the real content).
			if c.contextPrefix && path != "" {
				r.EmbedText = path + "\n\n" + text
			}
			results = append(results, r)
		}
	}
	return results, nil
}

// mdSection is a heading's section: its heading path and the section text
// (heading line plus body, excluding nested subsections).
type mdSection struct {
	path []string
	text string
}

// mdGroup is one or more adjacent sections packed toward the target size, under
// their common heading-path prefix.
type mdGroup struct {
	path []string
	text string
}

// merge greedily packs adjacent sections while their combined token count stays
// within size, so tiny sections don't become tiny chunks. A section that alone
// exceeds size is left in its own group (emitTexts splits it). A merged group's
// path is the longest common heading-path prefix of its members.
func (c MarkdownChunker) merge(sections []mdSection) []mdGroup {
	var groups []mdGroup
	for _, s := range sections {
		if len(groups) == 0 {
			groups = append(groups, mdGroup(s))
			continue
		}
		last := &groups[len(groups)-1]
		candidate := last.text + "\n\n" + s.text
		if c.countTokens(candidate) <= c.size {
			last.text = candidate
			last.path = commonPrefix(last.path, s.path)
		} else {
			groups = append(groups, mdGroup(s))
		}
	}
	return groups
}

// splitSections divides markdown into sections by ATX heading, tracking a
// heading stack for the path and ignoring headings inside fenced code blocks.
// Content before the first heading is a section with an empty path. Each
// section's text includes its own heading line, excluding nested subsections.
func splitSections(text string) []mdSection {
	var (
		sections          []mdSection
		stack, stackLevel = []string{}, []int{}
		cur               []string
		curPath           []string
		inFence           bool
		fenceCh           byte
		fenceLen          int
	)
	flush := func() {
		body := strings.Trim(strings.Join(cur, "\n"), "\n")
		if strings.TrimSpace(body) != "" {
			sections = append(sections, mdSection{path: append([]string(nil), curPath...), text: body})
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if ch, n, ok := fenceInfo(line); ok {
			switch {
			case !inFence:
				inFence, fenceCh, fenceLen = true, ch, n
			case ch == fenceCh && n >= fenceLen:
				inFence = false
			}
			cur = append(cur, line)
			continue
		}
		if !inFence {
			if level, heading, ok := headingInfo(line); ok {
				flush()
				for len(stackLevel) > 0 && stackLevel[len(stackLevel)-1] >= level {
					stackLevel = stackLevel[:len(stackLevel)-1]
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, heading)
				stackLevel = append(stackLevel, level)
				curPath = append([]string(nil), stack...)
				cur = append(cur, line)
				continue
			}
		}
		cur = append(cur, line)
	}
	flush()
	return sections
}

// headingInfo reports whether line is an ATX heading (after up to 3 leading
// spaces, 1–6 '#' then a space or end of line), returning its level and the
// heading text with any closing '#' run trimmed.
func headingInfo(line string) (level int, heading string, ok bool) {
	i := 0
	for i < len(line) && i < 4 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '#' {
		return 0, "", false
	}
	j := i
	for j < len(line) && line[j] == '#' {
		j++
	}
	level = j - i
	if level < 1 || level > 6 {
		return 0, "", false
	}
	if j < len(line) && line[j] != ' ' && line[j] != '\t' {
		return 0, "", false // e.g. "#hashtag" is not a heading
	}
	heading = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[j:]), "#"))
	return level, heading, true
}

// fenceInfo reports whether line opens or closes a fenced code block (after up
// to 3 leading spaces, a run of 3+ backticks or tildes), returning the fence
// character and run length so a closing fence can be matched to its opener.
func fenceInfo(line string) (ch byte, length int, ok bool) {
	i := 0
	for i < len(line) && i < 4 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, false
	}
	ch = line[i]
	j := i
	for j < len(line) && line[j] == ch {
		j++
	}
	if j-i < 3 {
		return 0, 0, false
	}
	return ch, j - i, true
}

// commonPrefix returns the longest common prefix of two heading paths.
func commonPrefix(a, b []string) []string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return append([]string(nil), a[:i]...)
}
