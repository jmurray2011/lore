package mcp

import (
	"fmt"
	"strings"
)

// These render a human-readable text block alongside each tool's structured
// content (MCP serves both: agents read StructuredContent, humans read the text).
// They are display-only; the structured output is the machine contract.

// askText renders an answer with its numbered sources for display.
func askText(out AskOutput, grounded bool) string {
	var b strings.Builder
	b.WriteString(out.Answer)
	if !grounded {
		b.WriteString("\n\n(not grounded — no chunks matched; answered from model knowledge alone)")
	}
	if len(out.Citations) > 0 {
		b.WriteString("\n\nSources:\n")
		for _, c := range out.Citations {
			label := fmt.Sprintf("%s#%d", shortLabel(c.Source), c.Seq)
			if c.Collection != "" {
				label = c.Collection + " · " + label
			}
			fmt.Fprintf(&b, "[%d] %s\n", c.Ordinal, label)
			if c.Text != "" {
				fmt.Fprintf(&b, "%s\n", c.Text)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// queryText renders retrieved hits as a compact list.
func queryText(hits []Hit) string {
	if len(hits) == 0 {
		return "no matching chunks"
	}
	var b strings.Builder
	for i, h := range hits {
		label := fmt.Sprintf("%s#%d", shortLabel(h.Source), h.Seq)
		if h.Collection != "" {
			label = h.Collection + " · " + label
		}
		score := fmt.Sprintf("%.4f", h.Score)
		if h.RerankScore != nil {
			score = fmt.Sprintf("sim=%.4f rerank=%.4f", h.Score, *h.RerankScore)
		}
		fmt.Fprintf(&b, "[%d] %s  (%s)\n%s\n\n", i+1, label, score, h.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// chunksText renders fetched chunks as labeled blocks.
func chunksText(chunks []Chunk) string {
	if len(chunks) == 0 {
		return "no chunks found"
	}
	var b strings.Builder
	for _, c := range chunks {
		if c.HeadingPath != "" {
			fmt.Fprintf(&b, "chunk %d — %s\n%s\n\n", c.Seq, c.HeadingPath, c.Text)
		} else {
			fmt.Fprintf(&b, "chunk %d\n%s\n\n", c.Seq, c.Text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// collectionsText renders the collection list as one line each.
func collectionsText(colls []CollectionInfo) string {
	if len(colls) == 0 {
		return "no collections"
	}
	var b strings.Builder
	for _, c := range colls {
		fmt.Fprintf(&b, "%s — %s/%d, %d documents, chunker %s\n", c.Name, c.Model, c.Dimensions, c.Documents, c.Chunker)
	}
	return strings.TrimRight(b.String(), "\n")
}

// statusText renders one collection's detailed status.
func statusText(s CollectionStatusOutput) string {
	text := fmt.Sprintf("%s\n  model: %s\n  dimensions: %d\n  documents: %d\n  chunks: %d\n  chunker: %s",
		s.Name, s.Model, s.Dimensions, s.Documents, s.Chunks, s.Chunker)
	if s.LastIngestAt != "" {
		text += "\n  last ingest: " + s.LastIngestAt
	}
	text += "\n  digest: " + s.CorpusDigest
	return text
}
