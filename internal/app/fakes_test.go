package app_test

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Small hand-written fakes for the ports the use cases consume. They favor
// canned data and recorded inputs over real behavior — adapter semantics are
// the conformance suites' job, not these tests'. The repository/index fakes are
// goroutine-safe because Ingest drives them concurrently.

var (
	_ app.CollectionRepository = (*fakeCollections)(nil)
	_ app.DocumentRepository   = (*fakeDocs)(nil)
	_ app.VectorIndex          = (*fakeIndex)(nil)
	_ app.Embedder             = (*fakeEmbedder)(nil)
	_ app.Generator            = (*fakeGenerator)(nil)
	_ app.Source               = (*fakeSource)(nil)
	_ app.Extractor            = (*fakeExtractor)(nil)
)

type fakeCollections struct {
	byName map[string]*domain.Collection
}

func newFakeCollections(cs ...*domain.Collection) *fakeCollections {
	m := make(map[string]*domain.Collection, len(cs))
	for _, c := range cs {
		m[c.Name] = c
	}
	return &fakeCollections{byName: m}
}

func (f *fakeCollections) Create(_ context.Context, c *domain.Collection) error {
	if _, ok := f.byName[c.Name]; ok {
		return app.ErrAlreadyExists
	}
	f.byName[c.Name] = c
	return nil
}

func (f *fakeCollections) Get(_ context.Context, name string) (*domain.Collection, error) {
	c, ok := f.byName[name]
	if !ok {
		return nil, app.ErrNotFound
	}
	return c, nil
}

func (f *fakeCollections) List(context.Context) ([]*domain.Collection, error) {
	out := make([]*domain.Collection, 0, len(f.byName))
	for _, c := range f.byName {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeCollections) Delete(_ context.Context, name string) error {
	if _, ok := f.byName[name]; !ok {
		return app.ErrNotFound
	}
	delete(f.byName, name)
	return nil
}

func (f *fakeCollections) RecordSource(_ context.Context, name, source string) error {
	c, ok := f.byName[name]
	if !ok {
		return app.ErrNotFound
	}
	for _, s := range c.Sources {
		if s == source {
			return nil
		}
	}
	c.Sources = append(c.Sources, source)
	return nil
}

type fakeEmbedder struct {
	space      domain.EmbeddingSpace
	byText     map[string][]float32
	spaceErr   error
	embedErr   error
	embedCalls atomic.Int64
	onEmbed    func() // optional hook invoked at the start of each Embed
}

func (f *fakeEmbedder) Space(context.Context) (domain.EmbeddingSpace, error) {
	return f.space, f.spaceErr
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.embedCalls.Add(1)
	if f.onEmbed != nil {
		f.onEmbed()
	}
	if f.embedErr != nil {
		return nil, f.embedErr
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := f.byText[t]; ok {
			out[i] = v
			continue
		}
		dims := f.space.Dimensions
		if dims <= 0 {
			dims = 1
		}
		v := make([]float32, dims)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

type fakeIndex struct {
	mu        sync.Mutex
	matches   map[string][]domain.VectorMatch         // canned Search results
	upserted  map[string]map[domain.ChunkID][]float32 // recorded Upserts per collection
	searchErr error
	upsertErr error

	gotCollection string
	gotQuery      []float32
	gotK          int
}

func (f *fakeIndex) Upsert(_ context.Context, collection string, entries []app.VectorEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	if f.upserted == nil {
		f.upserted = map[string]map[domain.ChunkID][]float32{}
	}
	col := f.upserted[collection]
	if col == nil {
		col = map[domain.ChunkID][]float32{}
		f.upserted[collection] = col
	}
	for _, e := range entries {
		col[e.ChunkID] = e.Vector
	}
	return nil
}

func (f *fakeIndex) Search(_ context.Context, collection string, query []float32, k int) ([]domain.VectorMatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotCollection, f.gotQuery, f.gotK = collection, query, k
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.matches[collection], nil
}

func (f *fakeIndex) Delete(_ context.Context, collection string, ids []domain.ChunkID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.upserted[collection], id)
	}
	return nil
}

func (f *fakeIndex) count(collection string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upserted[collection])
}

type fakeDocs struct {
	mu        sync.Mutex
	docs      map[domain.DocumentID]domain.Document
	chunks    map[domain.ChunkID]domain.Chunk
	getErr    error
	upsertErr error
}

func (f *fakeDocs) Upsert(_ context.Context, doc *domain.Document, chunks []domain.Chunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	if f.docs == nil {
		f.docs = map[domain.DocumentID]domain.Document{}
	}
	if f.chunks == nil {
		f.chunks = map[domain.ChunkID]domain.Chunk{}
	}
	f.docs[doc.ID] = *doc
	for _, c := range chunks {
		f.chunks[c.ID] = c
	}
	return nil
}

func (f *fakeDocs) GetBySource(_ context.Context, collection, sourceURI string) (*domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[domain.DeriveDocumentID(collection, sourceURI)]
	if !ok {
		return nil, app.ErrNotFound
	}
	return &d, nil
}

func (f *fakeDocs) GetChunks(_ context.Context, ids []domain.ChunkID) ([]domain.Chunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make([]domain.Chunk, 0, len(ids))
	for _, id := range ids {
		if c, ok := f.chunks[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeDocs) GetChunksByDocument(_ context.Context, collection string, id domain.DocumentID) ([]domain.Chunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if d, ok := f.docs[id]; !ok || d.Collection != collection {
		return nil, nil
	}
	var out []domain.Chunk
	for _, c := range f.chunks {
		if c.DocumentID == id {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (f *fakeDocs) GetDocuments(_ context.Context, ids []domain.DocumentID) ([]*domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := f.docs[id]; ok {
			doc := d
			out = append(out, &doc)
		}
	}
	return out, nil
}

func (f *fakeDocs) ListDocuments(_ context.Context, collection string) ([]*domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*domain.Document
	for _, d := range f.docs {
		if d.Collection == collection {
			doc := d
			out = append(out, &doc)
		}
	}
	return out, nil
}

func (f *fakeDocs) Delete(_ context.Context, collection string, id domain.DocumentID) ([]domain.ChunkID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[id]
	if !ok || d.Collection != collection {
		return nil, app.ErrNotFound
	}
	return f.removeLocked(id), nil
}

func (f *fakeDocs) DeleteCollection(_ context.Context, collection string) ([]domain.ChunkID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var removed []domain.ChunkID
	for id, d := range f.docs {
		if d.Collection == collection {
			removed = append(removed, f.removeLocked(id)...)
		}
	}
	return removed, nil
}

// removeLocked deletes a document and the chunks that belong to it (matched by
// DocumentID), returning the removed chunk IDs. Callers must hold f.mu.
func (f *fakeDocs) removeLocked(id domain.DocumentID) []domain.ChunkID {
	var ids []domain.ChunkID
	for cid, c := range f.chunks {
		if c.DocumentID == id {
			ids = append(ids, cid)
			delete(f.chunks, cid)
		}
	}
	delete(f.docs, id)
	return ids
}

type fakeGenerator struct {
	answer app.Answer
	err    error

	gotQuestion    string
	gotHits        []domain.ChunkHit
	gotAttachments []domain.Attachment
}

func (f *fakeGenerator) Synthesize(_ context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment) (app.Answer, error) {
	f.gotQuestion, f.gotHits, f.gotAttachments = question, hits, attachments
	return f.answer, f.err
}

type fakeSource struct {
	items []app.SourceItem
	err   error
}

func (f *fakeSource) Walk(ctx context.Context, _ string, fn func(app.SourceItem) error) error {
	for _, it := range f.items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(it); err != nil {
			return err
		}
	}
	return f.err
}

type fakeExtractor struct {
	unsupported map[string]bool // content types to reject; default supports all
	transform   func(contentType string, raw []byte) (string, error)
}

func (f *fakeExtractor) Supports(contentType string) bool { return !f.unsupported[contentType] }

func (f *fakeExtractor) Extract(contentType string, raw []byte) (string, error) {
	if f.transform != nil {
		return f.transform(contentType, raw)
	}
	return string(raw), nil
}
