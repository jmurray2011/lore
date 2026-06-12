package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestAsker(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	docID := domain.DeriveDocumentID("docs", "file:///a.md")
	c0 := mustChunk(t, docID, 0, "alpha")

	newAsker := func(gen *fakeGenerator, emb *fakeEmbedder, idx *fakeIndex, docs *fakeDocs) *app.Asker {
		coll := mustCollection(t, "docs", space)
		q := app.NewQuerier(newFakeCollections(coll), idx, docs, emb)
		return app.NewAsker(q, gen)
	}

	t.Run("synthesizes over retrieved hits", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{"docs": {{ChunkID: c0.ID, Score: 0.8}}}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"why": {1, 0, 0}}}
		gen := &fakeGenerator{answer: app.Answer{Text: "because", Citations: []domain.ChunkID{c0.ID}}}
		a := newAsker(gen, emb, idx, docs)

		ans, err := a.Ask(ctx, "docs", "why", 1)
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if ans.Text != "because" {
			t.Errorf("answer text = %q", ans.Text)
		}
		if gen.gotQuestion != "why" {
			t.Errorf("generator got question %q", gen.gotQuestion)
		}
		if len(gen.gotHits) != 1 || gen.gotHits[0].Chunk.ID != c0.ID {
			t.Errorf("generator got hits %+v", gen.gotHits)
		}
	})

	t.Run("propagates retrieval errors without calling the generator", func(t *testing.T) {
		gen := &fakeGenerator{}
		q := app.NewQuerier(newFakeCollections(), &fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: space})
		a := app.NewAsker(q, gen)

		if _, err := a.Ask(ctx, "missing", "why", 1); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
		if gen.gotQuestion != "" {
			t.Error("generator must not be called when retrieval fails")
		}
	})

	t.Run("propagates generator errors", func(t *testing.T) {
		idx := &fakeIndex{matches: map[string][]domain.VectorMatch{"docs": {{ChunkID: c0.ID, Score: 0.8}}}}
		docs := &fakeDocs{chunks: map[domain.ChunkID]domain.Chunk{c0.ID: c0}}
		emb := &fakeEmbedder{space: space, byText: map[string][]float32{"why": {1, 0, 0}}}
		llmDown := errors.New("llm down")
		gen := &fakeGenerator{err: llmDown}
		a := newAsker(gen, emb, idx, docs)

		if _, err := a.Ask(ctx, "docs", "why", 1); !errors.Is(err, llmDown) {
			t.Errorf("want wrapped llm error, got %v", err)
		}
	})
}
