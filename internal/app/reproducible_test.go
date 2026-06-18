package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// fakeDeterministicGen implements both Generator and DeterministicGenerator so a
// test can exercise the reproducible seam.
type fakeDeterministicGen struct {
	answer              app.Answer
	err                 error
	calledDeterministic bool
}

func (f *fakeDeterministicGen) Synthesize(_ context.Context, _ string, _ []domain.ChunkHit, _ []domain.Attachment) (app.Answer, error) {
	return f.answer, f.err
}

func (f *fakeDeterministicGen) SynthesizeDeterministic(_ context.Context, _ string, _ []domain.ChunkHit, _ []domain.Attachment) (app.Answer, error) {
	f.calledDeterministic = true
	return f.answer, f.err
}

func aHit(t *testing.T) domain.ChunkHit {
	t.Helper()
	c, err := domain.NewChunk(domain.DeriveDocumentID("docs", "file:///a.md"), 0, "body")
	if err != nil {
		t.Fatal(err)
	}
	return domain.ChunkHit{Chunk: c, Source: "file:///a.md"}
}

func TestAskerSynthesizeReproducible_UnsupportedGeneratorFailsClosed(t *testing.T) {
	t.Parallel()
	// A generator without the DeterministicGenerator capability must NOT silently
	// produce a non-deterministic answer the caller would stamp "reproducible".
	a := app.NewAsker(nil, &fakeGenerator{answer: app.Answer{Text: "would be non-deterministic"}})
	_, err := a.SynthesizeReproducible(context.Background(), "q", []domain.ChunkHit{aHit(t)}, nil)
	if !errors.Is(err, app.ErrReproducibleUnsupported) {
		t.Fatalf("want ErrReproducibleUnsupported, got %v", err)
	}
}

func TestAskerSynthesizeReproducible_UsesDeterministicSeam(t *testing.T) {
	t.Parallel()
	gen := &fakeDeterministicGen{answer: app.Answer{Text: "pinned", Provenance: &app.Provenance{Deterministic: true, Seed: 1}}}
	a := app.NewAsker(nil, gen)
	ans, err := a.SynthesizeReproducible(context.Background(), "q", []domain.ChunkHit{aHit(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !gen.calledDeterministic {
		t.Error("SynthesizeReproducible must route through the deterministic seam")
	}
	if ans.Text != "pinned" || ans.Provenance == nil || !ans.Provenance.Deterministic {
		t.Errorf("answer = %+v, want pinned + deterministic provenance", ans)
	}
	if !ans.Grounded {
		t.Error("an answer over a hit should be Grounded")
	}
}
