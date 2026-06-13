package mcp

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Tool descriptions are load-bearing: the calling model decides whether and when
// to invoke a tool from these strings and the per-field jsonschema descriptions.

const askDescription = "Answer a question using ONLY the documents indexed in the named lore collection(s). " +
	"Returns a grounded answer with citations to the exact source passages (each citation carries the chunk's " +
	"text by default, so claims are verifiable without a second call). Use this when the user asks about the " +
	"content of a specific document corpus rather than for general knowledge."

const queryDescription = "Retrieve the document chunks most semantically similar to a query from the named lore " +
	"collection(s), WITHOUT synthesizing an answer. Returns the raw ranked passages (text, source, score, chunk_id, " +
	"collection) so the caller can read or post-process the evidence itself. Use this when you want source material " +
	"rather than a written answer."

const getChunksDescription = "Fetch specific document chunks by their chunk_id from a lore collection — the exact " +
	"passages cited by `ask` or returned by `query`. Use this to retrieve the full text behind a citation. " +
	"Unknown chunk IDs are omitted from the result rather than erroring."

const listCollectionsDescription = "List the lore collections this server exposes, each with its document count, " +
	"embedding model, dimensions, and chunker. Use this to discover which corpora are available before calling " +
	"`ask` or `query`."

const collectionStatusDescription = "Show detailed status for one lore collection: its embedding model, " +
	"dimensions, chunker, document count, and chunk count. Use this to inspect a single corpus in depth."

// ---- ask ----

// AskInput is the `ask` tool's arguments.
type AskInput struct {
	Collections    []string `json:"collections" jsonschema:"the lore collection name(s) to ground the answer in; at least one, all sharing one embedding space"`
	Question       string   `json:"question" jsonschema:"the question to answer using only these collections"`
	K              int      `json:"k,omitempty" jsonschema:"number of chunks to ground on (default 8)"`
	Budget         int      `json:"budget,omitempty" jsonschema:"cap the grounding to roughly this many tokens, trimming within k (0 = no cap)"`
	Rerank         bool     `json:"rerank,omitempty" jsonschema:"two-stage retrieval: search a wide vector pool then cross-encoder rerank to the top k (requires a configured rerank provider)"`
	SourceGlob     string   `json:"source_glob,omitempty" jsonschema:"restrict grounding to documents whose source matches this glob, e.g. '*.pdf'"`
	Strict         bool     `json:"strict,omitempty" jsonschema:"when true, return an error instead of an ungrounded answer if no chunks match"`
	IncludeSources *bool    `json:"include_sources,omitempty" jsonschema:"include each cited chunk's full text in the citations (default true)"`
}

// Citation is one cited chunk in an answer.
type Citation struct {
	Ordinal    int    `json:"ordinal" jsonschema:"the [n] marker this citation has in the answer text"`
	Source     string `json:"source" jsonschema:"source URI of the cited document"`
	Seq        int    `json:"seq" jsonschema:"the chunk's position within its document"`
	ChunkID    string `json:"chunk_id" jsonschema:"the cited chunk's ID; pass to get_chunks to fetch it"`
	Collection string `json:"collection,omitempty" jsonschema:"origin collection (set only for multi-collection answers)"`
	Text       string `json:"text,omitempty" jsonschema:"the cited chunk's text (present when include_sources is true)"`
}

// AskOutput is the `ask` tool's structured result.
type AskOutput struct {
	Answer    string     `json:"answer" jsonschema:"the synthesized answer; inline [n] markers reference the citations"`
	Citations []Citation `json:"citations" jsonschema:"the chunks the answer cites, in [n] order"`
	Grounded  bool       `json:"grounded" jsonschema:"false means nothing matched and the answer rests on model knowledge alone"`
}

func (s *Server) ask(ctx context.Context, _ *mcpsdk.CallToolRequest, in AskInput) (*mcpsdk.CallToolResult, AskOutput, error) {
	collections := dedup(in.Collections)
	if len(collections) == 0 {
		return nil, AskOutput{}, errNoCollections
	}
	if strings.TrimSpace(in.Question) == "" {
		return nil, AskOutput{}, errEmptyQuestion
	}
	if err := s.scope.check(collections...); err != nil {
		return nil, AskOutput{}, err
	}

	hits, err := s.resolveHits(ctx, collections, in.Question, in.K, in.SourceGlob, in.Rerank)
	if err != nil {
		return nil, AskOutput{}, err
	}
	if in.Budget > 0 {
		hits = s.budgetTrim(hits, in.Budget)
	}
	// Replicate Ask's strict guard (the Synthesize seam has none — the caller
	// chose the hits): nothing to ground on under strict is an error, and the
	// model is not called.
	if len(hits) == 0 && in.Strict {
		return nil, AskOutput{}, fmt.Errorf("%w: no chunks matched %s", app.ErrNoGrounding, strings.Join(collections, ", "))
	}

	ans, err := s.deps.Ask.Synthesize(ctx, in.Question, hits, nil)
	if err != nil {
		return nil, AskOutput{}, err
	}

	includeText := in.IncludeSources == nil || *in.IncludeSources
	out, err := s.askOutput(ctx, ans, collections, includeText)
	if err != nil {
		return nil, AskOutput{}, err
	}
	return textResult(askText(out, ans.Grounded)), out, nil
}

// askOutput builds the structured answer: the prose with [chunkID] markers
// renumbered to [n] (first-appearance order), and the citations in that order —
// with each cited chunk's text attached when includeText is set.
func (s *Server) askOutput(ctx context.Context, ans app.Answer, collections []string, includeText bool) (AskOutput, error) {
	text, order := renumber(ans)
	out := AskOutput{Answer: text, Grounded: ans.Grounded, Citations: make([]Citation, len(order))}

	textByChunk := map[domain.ChunkID]string{}
	if includeText && len(order) > 0 {
		var err error
		textByChunk, err = s.citedText(ctx, order, collections)
		if err != nil {
			return AskOutput{}, err
		}
	}
	for i, c := range order {
		out.Citations[i] = Citation{
			Ordinal:    i + 1,
			Source:     c.Source,
			Seq:        c.Seq,
			ChunkID:    string(c.ChunkID),
			Collection: c.Collection,
			Text:       textByChunk[c.ChunkID],
		}
	}
	return out, nil
}

// citedText fetches the text of the cited chunks via the existing Catalog use
// case, grouping by origin collection for multi-collection answers (mirrors the
// CLI's expansionChunks). Single-collection citations carry no Collection, so
// they resolve against the sole requested collection.
func (s *Server) citedText(ctx context.Context, citations []domain.Citation, collections []string) (map[domain.ChunkID]string, error) {
	byColl := map[string][]string{}
	for _, c := range citations {
		coll := c.Collection
		if coll == "" {
			coll = collections[0]
		}
		byColl[coll] = append(byColl[coll], string(c.ChunkID))
	}
	out := make(map[domain.ChunkID]string, len(citations))
	for coll, ids := range byColl {
		chunks, err := s.deps.Catalog.ChunksByIDs(ctx, coll, ids)
		if err != nil {
			return nil, err
		}
		for _, ch := range chunks {
			out[ch.ID] = ch.Text
		}
	}
	return out, nil
}

// ---- query ----

// QueryInput is the `query` tool's arguments.
type QueryInput struct {
	Collections []string `json:"collections" jsonschema:"the lore collection name(s) to search; at least one, all sharing one embedding space"`
	Query       string   `json:"query" jsonschema:"the search text"`
	K           int      `json:"k,omitempty" jsonschema:"number of chunks to return (default 8)"`
	SourceGlob  string   `json:"source_glob,omitempty" jsonschema:"restrict to documents whose source matches this glob, e.g. '*.pdf'"`
	Rerank      bool     `json:"rerank,omitempty" jsonschema:"two-stage retrieval: search a wide vector pool then cross-encoder rerank to the top k (requires a configured rerank provider)"`
}

// Hit is one retrieved chunk in the query result (the standard lore hit object
// plus its origin collection).
type Hit struct {
	Text        string   `json:"text" jsonschema:"the chunk's text"`
	Source      string   `json:"source" jsonschema:"source URI of the chunk's document"`
	Score       float64  `json:"score" jsonschema:"vector similarity score (higher is more similar)"`
	ChunkID     string   `json:"chunk_id" jsonschema:"the chunk's ID; pass to get_chunks to fetch it again"`
	Seq         int      `json:"seq" jsonschema:"the chunk's position within its document"`
	Collection  string   `json:"collection,omitempty" jsonschema:"origin collection (set only for multi-collection queries)"`
	RerankScore *float64 `json:"rerank_score,omitempty" jsonschema:"cross-encoder relevance score (present only when rerank was used)"`
}

// QueryOutput is the `query` tool's structured result.
type QueryOutput struct {
	Hits []Hit `json:"hits" jsonschema:"the matching chunks, best first"`
}

func (s *Server) query(ctx context.Context, _ *mcpsdk.CallToolRequest, in QueryInput) (*mcpsdk.CallToolResult, QueryOutput, error) {
	collections := dedup(in.Collections)
	if len(collections) == 0 {
		return nil, QueryOutput{}, errNoCollections
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, QueryOutput{}, errEmptyQuery
	}
	if err := s.scope.check(collections...); err != nil {
		return nil, QueryOutput{}, err
	}

	hits, err := s.resolveHits(ctx, collections, in.Query, in.K, in.SourceGlob, in.Rerank)
	if err != nil {
		return nil, QueryOutput{}, err
	}

	out := QueryOutput{Hits: make([]Hit, len(hits))}
	for i, h := range hits {
		out.Hits[i] = Hit{
			Text:        h.Chunk.Text,
			Source:      h.Source,
			Score:       h.Score,
			ChunkID:     string(h.Chunk.ID),
			Seq:         h.Chunk.Seq,
			Collection:  h.Collection,
			RerankScore: h.RerankScore,
		}
	}
	return textResult(queryText(out.Hits)), out, nil
}

// ---- get_chunks ----

// GetChunksInput is the `get_chunks` tool's arguments.
type GetChunksInput struct {
	Collection string   `json:"collection" jsonschema:"the lore collection the chunks belong to"`
	ChunkIDs   []string `json:"chunk_ids" jsonschema:"the chunk IDs to fetch; unknown IDs are omitted from the result"`
}

// Chunk is one stored chunk returned by get_chunks (the cat --chunk shape).
type Chunk struct {
	ChunkID     string `json:"chunk_id" jsonschema:"the chunk's ID"`
	Seq         int    `json:"seq" jsonschema:"the chunk's position within its document"`
	HeadingPath string `json:"heading_path,omitempty" jsonschema:"the document section the chunk came from (structure-aware chunkers only)"`
	Text        string `json:"text" jsonschema:"the chunk's stored text"`
}

// GetChunksOutput is the `get_chunks` tool's structured result.
type GetChunksOutput struct {
	Chunks []Chunk `json:"chunks" jsonschema:"the found chunks, in request order; absent IDs are omitted"`
}

func (s *Server) getChunks(ctx context.Context, _ *mcpsdk.CallToolRequest, in GetChunksInput) (*mcpsdk.CallToolResult, GetChunksOutput, error) {
	if strings.TrimSpace(in.Collection) == "" {
		return nil, GetChunksOutput{}, errNoCollection
	}
	if len(in.ChunkIDs) == 0 {
		return nil, GetChunksOutput{}, errNoChunkIDs
	}
	if err := s.scope.check(in.Collection); err != nil {
		return nil, GetChunksOutput{}, err
	}

	// Mirror cat --chunk: an unknown collection errors (ErrNotFound), but absent
	// chunk IDs are simply omitted rather than failing the call.
	chunks, err := s.deps.Catalog.ChunksByIDs(ctx, in.Collection, in.ChunkIDs)
	if err != nil {
		return nil, GetChunksOutput{}, err
	}
	out := GetChunksOutput{Chunks: make([]Chunk, len(chunks))}
	for i, c := range chunks {
		out.Chunks[i] = Chunk{ChunkID: string(c.ID), Seq: c.Seq, HeadingPath: c.HeadingPath, Text: c.Text}
	}
	return textResult(chunksText(out.Chunks)), out, nil
}

// ---- list_collections ----

// CollectionInfo is one collection's summary in list_collections.
type CollectionInfo struct {
	Name       string `json:"name" jsonschema:"the collection name"`
	Model      string `json:"model" jsonschema:"the embedding model the collection is pinned to"`
	Dimensions int    `json:"dimensions" jsonschema:"the embedding dimensionality"`
	Documents  int    `json:"documents" jsonschema:"number of documents ingested"`
	Chunker    string `json:"chunker" jsonschema:"the pinned chunker spec"`
}

// ListCollectionsInput is empty — list_collections takes no arguments.
type ListCollectionsInput struct{}

// ListCollectionsOutput is the `list_collections` tool's structured result.
type ListCollectionsOutput struct {
	Collections []CollectionInfo `json:"collections" jsonschema:"the collections this server exposes"`
}

func (s *Server) listCollections(ctx context.Context, _ *mcpsdk.CallToolRequest, _ ListCollectionsInput) (*mcpsdk.CallToolResult, ListCollectionsOutput, error) {
	colls, err := s.deps.Catalog.List(ctx)
	if err != nil {
		return nil, ListCollectionsOutput{}, err
	}
	var out ListCollectionsOutput
	for _, c := range colls {
		if !s.scope.allows(c.Name) {
			continue
		}
		docs, err := s.deps.Catalog.ListDocuments(ctx, c.Name)
		if err != nil {
			return nil, ListCollectionsOutput{}, err
		}
		out.Collections = append(out.Collections, collectionInfo(c, len(docs)))
	}
	sort.Slice(out.Collections, func(i, j int) bool { return out.Collections[i].Name < out.Collections[j].Name })
	return textResult(collectionsText(out.Collections)), out, nil
}

// ---- collection_status ----

// CollectionStatusInput is the `collection_status` tool's arguments.
type CollectionStatusInput struct {
	Collection string `json:"collection" jsonschema:"the collection to inspect"`
}

// CollectionStatusOutput is one collection's detailed status, including its chunk
// count (the field list_collections omits to stay cheap).
type CollectionStatusOutput struct {
	Name       string `json:"name" jsonschema:"the collection name"`
	Model      string `json:"model" jsonschema:"the embedding model the collection is pinned to"`
	Dimensions int    `json:"dimensions" jsonschema:"the embedding dimensionality"`
	Documents  int    `json:"documents" jsonschema:"number of documents ingested"`
	Chunks     int    `json:"chunks" jsonschema:"number of indexed chunks"`
	Chunker    string `json:"chunker" jsonschema:"the pinned chunker spec"`
}

func (s *Server) collectionStatus(ctx context.Context, _ *mcpsdk.CallToolRequest, in CollectionStatusInput) (*mcpsdk.CallToolResult, CollectionStatusOutput, error) {
	if strings.TrimSpace(in.Collection) == "" {
		return nil, CollectionStatusOutput{}, errNoCollection
	}
	if err := s.scope.check(in.Collection); err != nil {
		return nil, CollectionStatusOutput{}, err
	}
	coll, err := s.deps.Catalog.Get(ctx, in.Collection)
	if err != nil {
		return nil, CollectionStatusOutput{}, err
	}
	docs, err := s.deps.Catalog.ListDocuments(ctx, in.Collection)
	if err != nil {
		return nil, CollectionStatusOutput{}, err
	}
	entries, err := s.deps.Index.Entries(ctx, in.Collection)
	if err != nil {
		return nil, CollectionStatusOutput{}, err
	}
	info := collectionInfo(coll, len(docs))
	out := CollectionStatusOutput{
		Name:       info.Name,
		Model:      info.Model,
		Dimensions: info.Dimensions,
		Documents:  info.Documents,
		Chunks:     len(entries),
		Chunker:    info.Chunker,
	}
	return textResult(statusText(out)), out, nil
}

// ---- shared helpers ----

// citeRefRE matches bracketed citations like [<chunkID>] (possibly several
// comma-separated IDs), mirroring the CLI's parser.
var citeRefRE = regexp.MustCompile(`\[([^\[\]]+)\]`)

// renumber rewrites the model's inline [chunkID] references to compact numbered
// [n] markers (first-appearance order, deduped) and returns the rewritten prose
// plus the cited citations in that order — citations never referenced inline
// (e.g. from structured output) are appended. It is the MCP analogue of the
// CLI's numberedAnswer, so an answer's ordinals match its citations.
func renumber(ans app.Answer) (string, []domain.Citation) {
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

// collectionInfo summarizes a collection for the list/status tools.
func collectionInfo(c *domain.Collection, docs int) CollectionInfo {
	return CollectionInfo{
		Name:       c.Name,
		Model:      c.Space.Model,
		Dimensions: c.Space.Dimensions,
		Documents:  docs,
		Chunker:    chunkerLabel(c),
	}
}

// chunkerLabel renders a collection's pinned chunker, naming the read-only state
// of a collection that predates chunker pinning (mirrors the CLI's chunkerLabel).
func chunkerLabel(c *domain.Collection) string {
	if c.Chunker.IsZero() {
		return "unpinned (legacy; rebuild to ingest)"
	}
	return c.Chunker.String()
}

// shortLabel reduces a source URI to its basename for the human text block.
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

// textResult wraps a human-readable string as the tool result's text content; the
// SDK fills StructuredContent from the typed Out value alongside it.
func textResult(text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}
}
