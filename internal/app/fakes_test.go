package app_test

import (
	"context"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Small hand-written fakes for the ports the use cases consume. They favor
// canned data and recorded inputs over real behavior — adapter semantics are
// the conformance suites' job, not these tests'.

var (
	_ app.CollectionRepository = (*fakeCollections)(nil)
	_ app.DocumentRepository   = (*fakeDocs)(nil)
	_ app.VectorIndex          = (*fakeIndex)(nil)
	_ app.Embedder             = (*fakeEmbedder)(nil)
	_ app.Generator            = (*fakeGenerator)(nil)
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

type fakeEmbedder struct {
	space    domain.EmbeddingSpace
	byText   map[string][]float32
	spaceErr error
	embedErr error
}

func (f *fakeEmbedder) Space(context.Context) (domain.EmbeddingSpace, error) {
	return f.space, f.spaceErr
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.embedErr != nil {
		return nil, f.embedErr
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.byText[t]
	}
	return out, nil
}

type fakeIndex struct {
	matches   map[string][]domain.VectorMatch // collection -> matches
	searchErr error

	gotCollection string
	gotQuery      []float32
	gotK          int
}

func (f *fakeIndex) Upsert(context.Context, string, []app.VectorEntry) error { return nil }

func (f *fakeIndex) Search(_ context.Context, collection string, query []float32, k int) ([]domain.VectorMatch, error) {
	f.gotCollection, f.gotQuery, f.gotK = collection, query, k
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.matches[collection], nil
}

func (f *fakeIndex) Delete(context.Context, string, []domain.ChunkID) error { return nil }

type fakeDocs struct {
	chunks map[domain.ChunkID]domain.Chunk
	getErr error
}

func (f *fakeDocs) Upsert(context.Context, *domain.Document, []domain.Chunk) error { return nil }

func (f *fakeDocs) GetBySource(context.Context, string, string) (*domain.Document, error) {
	return nil, app.ErrNotFound
}

func (f *fakeDocs) GetChunks(_ context.Context, ids []domain.ChunkID) ([]domain.Chunk, error) {
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

func (f *fakeDocs) Delete(context.Context, string, domain.DocumentID) error { return nil }

type fakeGenerator struct {
	answer app.Answer
	err    error

	gotQuestion string
	gotHits     []domain.ChunkHit
}

func (f *fakeGenerator) Synthesize(_ context.Context, question string, hits []domain.ChunkHit) (app.Answer, error) {
	f.gotQuestion, f.gotHits = question, hits
	return f.answer, f.err
}
