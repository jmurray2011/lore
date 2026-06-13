package domain

// TextChunker splits plain text into token-sized chunks at paragraph (then
// sentence, then word) boundaries, never mid-sentence where avoidable. It has no
// heading awareness: it is the structure strategy's chunker for plain text and
// the default fallback for formats without their own structure-aware chunker
// (docx paragraphs; best-effort pdf/xlsx text). Deterministic and pure.
//
// Construct with NewTextChunker; the zero value is not usable.
type TextChunker struct {
	tokenPacker
}

// compile-time interface check
var _ Chunker = TextChunker{}

// NewTextChunker returns a TextChunker targeting size tokens per chunk with
// overlap tokens shared between size-driven splits. It requires size > 0,
// 0 <= overlap < size, and a non-nil token counter.
func NewTextChunker(size, overlap int, countTokens func(string) int) (TextChunker, error) {
	p, err := newTokenPacker("text", size, overlap, countTokens)
	if err != nil {
		return TextChunker{}, err
	}
	return TextChunker{tokenPacker: p}, nil
}

// Chunk packs the document text into token-sized chunks. There is no heading
// path; ContentType is ignored.
func (c TextChunker) Chunk(doc ParsedDoc) ([]ChunkResult, error) {
	texts := c.emitTexts(doc.Text)
	results := make([]ChunkResult, len(texts))
	for i, t := range texts {
		results[i] = ChunkResult{Text: t}
	}
	return results, nil
}
