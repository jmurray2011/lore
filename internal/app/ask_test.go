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
		gen := &fakeGenerator{answer: app.Answer{Text: "because", Citations: []domain.Citation{{ChunkID: c0.ID, Source: "file:///a.md", Seq: 0}}}}
		a := newAsker(gen, emb, idx, docs)

		att, err := domain.NewAttachment("image/png", "c.png", []byte{1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		ans, err := a.Ask(ctx, "docs", "why", 1, []domain.Attachment{att}, false, "")
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if ans.Text != "because" {
			t.Errorf("answer text = %q", ans.Text)
		}
		if !ans.Grounded {
			t.Error("an answer over real hits should be grounded")
		}
		if gen.gotQuestion != "why" {
			t.Errorf("generator got question %q", gen.gotQuestion)
		}
		if len(gen.gotHits) != 1 || gen.gotHits[0].Chunk.ID != c0.ID {
			t.Errorf("generator got hits %+v", gen.gotHits)
		}
		if len(gen.gotAttachments) != 1 || gen.gotAttachments[0].Name != "c.png" {
			t.Errorf("generator got attachments %+v", gen.gotAttachments)
		}
	})

	t.Run("propagates retrieval errors without calling the generator", func(t *testing.T) {
		gen := &fakeGenerator{}
		q := app.NewQuerier(newFakeCollections(), &fakeIndex{}, &fakeDocs{}, &fakeEmbedder{space: space})
		a := app.NewAsker(q, gen)

		if _, err := a.Ask(ctx, "missing", "why", 1, nil, false, ""); !errors.Is(err, app.ErrNotFound) {
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

		if _, err := a.Ask(ctx, "docs", "why", 1, nil, false, ""); !errors.Is(err, llmDown) {
			t.Errorf("want wrapped llm error, got %v", err)
		}
	})

	t.Run("strict mode refuses an ungrounded question without calling the generator", func(t *testing.T) {
		// No matching chunks and no attachments → nothing to ground on.
		gen := &fakeGenerator{}
		emb := &fakeEmbedder{space: space}
		a := newAsker(gen, emb, &fakeIndex{}, &fakeDocs{})

		if _, err := a.Ask(ctx, "docs", "why", 1, nil, true, ""); !errors.Is(err, app.ErrNoGrounding) {
			t.Errorf("want ErrNoGrounding, got %v", err)
		}
		if gen.gotQuestion != "" {
			t.Error("strict mode must not call the generator when grounding is empty")
		}
	})

	t.Run("non-strict proceeds ungrounded and marks the answer not grounded", func(t *testing.T) {
		gen := &fakeGenerator{answer: app.Answer{Text: "guess"}}
		emb := &fakeEmbedder{space: space}
		a := newAsker(gen, emb, &fakeIndex{}, &fakeDocs{})

		ans, err := a.Ask(ctx, "docs", "why", 1, nil, false, "")
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if ans.Grounded {
			t.Error("an answer with no hits and no attachments must be marked ungrounded")
		}
		if gen.gotQuestion != "why" {
			t.Error("non-strict mode should still call the generator")
		}
	})

	t.Run("attachments alone count as grounding even under strict with k=0", func(t *testing.T) {
		gen := &fakeGenerator{answer: app.Answer{Text: "from the image"}}
		emb := &fakeEmbedder{space: space}
		a := newAsker(gen, emb, &fakeIndex{}, &fakeDocs{})
		att, err := domain.NewAttachment("image/png", "c.png", []byte{1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}

		ans, err := a.Ask(ctx, "docs", "why", 0, []domain.Attachment{att}, true, "")
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if !ans.Grounded {
			t.Error("attachments should ground the answer")
		}
	})
}
