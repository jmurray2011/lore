package domain

import (
	"fmt"
	"strings"
)

// tokenPacker holds the token-sizing parameters and the boundary-respecting
// splitter shared by the structure-aware chunkers (markdown, plain text). It
// packs text into chunks of at most size tokens, preferring paragraph then
// sentence boundaries and only hard-splitting words as a last resort. Sizing is
// in real tokens via an injected counter, keeping the tokenizer dependency out
// of stdlib-only domain.
type tokenPacker struct {
	size        int
	overlap     int
	countTokens func(string) int
}

// newTokenPacker validates the shared sizing parameters: size > 0,
// 0 <= overlap < size, and a non-nil token counter.
func newTokenPacker(what string, size, overlap int, countTokens func(string) int) (tokenPacker, error) {
	switch {
	case size <= 0:
		return tokenPacker{}, fmt.Errorf("%s chunker: %w: size must be positive, got %d", what, ErrInvalidArgument, size)
	case overlap < 0:
		return tokenPacker{}, fmt.Errorf("%s chunker: %w: overlap must be non-negative, got %d", what, ErrInvalidArgument, overlap)
	case overlap >= size:
		return tokenPacker{}, fmt.Errorf("%s chunker: %w: overlap (%d) must be less than size (%d)", what, ErrInvalidArgument, overlap, size)
	case countTokens == nil:
		return tokenPacker{}, fmt.Errorf("%s chunker: %w: token counter is required", what, ErrInvalidArgument)
	}
	return tokenPacker{size: size, overlap: overlap, countTokens: countTokens}, nil
}

// emitTexts returns text as one chunk if it fits, otherwise splits it at
// paragraph boundaries (recursing to sentences then words for paragraphs that
// themselves exceed size), never breaking inside a fenced code block.
func (p tokenPacker) emitTexts(text string) []string {
	if c := strings.TrimSpace(text); c == "" {
		return nil
	}
	if p.countTokens(text) <= p.size {
		return []string{text}
	}
	return p.pack(splitParagraphs(text), "\n\n", p.splitParagraph)
}

// splitParagraph splits one oversized paragraph at sentence boundaries, falling
// back to token-windowed words for a single oversized sentence.
func (p tokenPacker) splitParagraph(para string) []string {
	return p.pack(splitSentences(para), " ", p.wordWindows)
}

// pack greedily joins pieces with sep into chunks of at most size tokens. A
// single piece that alone exceeds size is handed to finer, which splits it
// further; finer's output is emitted as its own chunks.
func (p tokenPacker) pack(pieces []string, sep string, finer func(string) []string) []string {
	var out []string
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			out = append(out, strings.Join(buf, sep))
			buf = nil
		}
	}
	for _, piece := range pieces {
		if p.countTokens(piece) > p.size {
			flush()
			out = append(out, finer(piece)...)
			continue
		}
		candidate := piece
		if len(buf) > 0 {
			candidate = strings.Join(buf, sep) + sep + piece
		}
		if p.countTokens(candidate) > p.size {
			flush()
		}
		buf = append(buf, piece)
	}
	flush()
	return out
}

// wordWindows hard-splits text into token-windowed word runs of at most size
// tokens, consecutive windows sharing up to overlap words. The last resort when
// a single sentence exceeds the chunk size; always makes progress.
func (p tokenPacker) wordWindows(text string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var out []string
	for start := 0; start < len(words); {
		end := start
		for end < len(words) {
			if end > start && p.countTokens(strings.Join(words[start:end+1], " ")) > p.size {
				break
			}
			end++
		}
		if end == start {
			end = start + 1 // a single oversized word still advances
		}
		out = append(out, strings.Join(words[start:end], " "))
		if end >= len(words) {
			break
		}
		step := end - start - p.overlap
		if step < 1 {
			step = 1
		}
		start += step
	}
	return out
}

// splitParagraphs splits text at blank lines, treating fenced code blocks as
// opaque (a blank line inside a fence does not split). Empty paragraphs are
// dropped.
func splitParagraphs(text string) []string {
	var (
		paras    []string
		cur      []string
		inFence  bool
		fenceCh  byte
		fenceLen int
	)
	flush := func() {
		p := strings.Trim(strings.Join(cur, "\n"), "\n")
		if strings.TrimSpace(p) != "" {
			paras = append(paras, p)
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
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return paras
}

// splitSentences splits text at sentence terminators (. ! ?) followed by
// whitespace or end of text. A crude heuristic, used only to break an oversized
// single paragraph; text with no detectable boundary is returned as one piece.
func splitSentences(text string) []string {
	var (
		out []string
		b   strings.Builder
	)
	runes := []rune(text)
	for i, r := range runes {
		b.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			if i+1 >= len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\t' {
				if s := strings.TrimSpace(b.String()); s != "" {
					out = append(out, s)
				}
				b.Reset()
			}
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}
