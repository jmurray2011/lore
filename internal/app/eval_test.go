package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestParseEvalSet(t *testing.T) {
	t.Run("parses a header then cases", func(t *testing.T) {
		in := `{"version": 1}
{"question": "what is auth?", "expected_sources": ["a.md"]}
{"question": "how to rotate?", "expected_chunks": ["c1"]}
`
		cases, err := app.ParseEvalSet(strings.NewReader(in))
		if err != nil {
			t.Fatalf("ParseEvalSet: %v", err)
		}
		if len(cases) != 2 || cases[0].Question != "what is auth?" || cases[1].ExpectedChunks[0] != "c1" {
			t.Errorf("cases = %+v", cases)
		}
	})

	t.Run("works without a header (defaults to v1)", func(t *testing.T) {
		cases, err := app.ParseEvalSet(strings.NewReader(`{"question": "q"}` + "\n"))
		if err != nil || len(cases) != 1 {
			t.Fatalf("cases=%+v err=%v", cases, err)
		}
	})

	t.Run("rejects a newer version", func(t *testing.T) {
		_, err := app.ParseEvalSet(strings.NewReader(`{"version": 999}` + "\n" + `{"question":"q"}`))
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("rejects malformed and empty inputs", func(t *testing.T) {
		for _, in := range []string{`not json`, `{"expected_sources":["a"]}`, ``} {
			if _, err := app.ParseEvalSet(strings.NewReader(in)); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("ParseEvalSet(%q): want ErrInvalidArgument, got %v", in, err)
			}
		}
	})
}

func TestEvaluator(t *testing.T) {
	ctx := context.Background()
	space := testSpace()
	coll := mustCollection(t, "docs", space)
	doc, _ := domain.NewDocument("docs", "file:///a.md", domain.HashContent([]byte("x")), time.Unix(0, 0).UTC())
	ca := mustChunk(t, doc.ID, 0, "Auth uses API keys.")
	cb := mustChunk(t, doc.ID, 1, "Unrelated text.")
	docs := &fakeDocs{
		docs:   map[domain.DocumentID]domain.Document{doc.ID: *doc},
		chunks: map[domain.ChunkID]domain.Chunk{ca.ID: ca, cb.ID: cb},
	}
	idx := &fakeIndex{matches: map[string][]domain.VectorMatch{
		"docs": {{ChunkID: ca.ID, Score: 0.9}, {ChunkID: cb.ID, Score: 0.4}},
	}}
	emb := &fakeEmbedder{space: space, byText: map[string][]float32{"how does auth work?": {1, 0, 0}}}
	querier := app.NewQuerier(newFakeCollections(coll), idx, docs, emb, &fakeLexical{})

	t.Run("retrieval metrics over expected chunks", func(t *testing.T) {
		ev := app.NewEvaluator(querier, nil, nil)
		cases := []app.EvalCase{{Question: "how does auth work?", ExpectedChunks: []string{string(ca.ID)}}}
		report, err := ev.Evaluate(ctx, "docs", cases, 2, false)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if report.Aggregates[app.MetricRecall] != 1 || report.Aggregates[app.MetricHitRate] != 1 || report.Aggregates[app.MetricMRR] != 1 {
			t.Errorf("aggregates = %+v (want recall/hit/mrr 1: ca is the top hit)", report.Aggregates)
		}
		if report.Aggregates[app.MetricPrecision] != 0.5 {
			t.Errorf("precision@2 = %v, want 0.5 (1 of 2 retrieved relevant)", report.Aggregates[app.MetricPrecision])
		}
	})

	t.Run("verification produces a support rate", func(t *testing.T) {
		gen := &fakeGenerator{answer: app.Answer{
			Text:      "Auth uses API keys [" + string(ca.ID) + "].",
			Citations: []domain.Citation{{ChunkID: ca.ID, Source: "file:///a.md", Seq: 0}},
		}}
		asker := app.NewAsker(querier, gen)
		catalog := app.NewCatalog(newFakeCollections(coll), docs, emb, domain.Registry{})
		checker := app.NewChecker(&fakeVerifier{}, catalog) // default: supported
		ev := app.NewEvaluator(querier, asker, checker)

		cases := []app.EvalCase{{Question: "how does auth work?"}}
		report, err := ev.Evaluate(ctx, "docs", cases, 2, true)
		if err != nil {
			t.Fatalf("Evaluate verify: %v", err)
		}
		if report.Aggregates[app.MetricSupportRate] != 1 {
			t.Errorf("support rate = %v, want 1 (the one claim is supported)", report.Aggregates[app.MetricSupportRate])
		}
		if len(report.Cases) != 1 || !report.Cases[0].HasVerify {
			t.Errorf("case should carry verification, got %+v", report.Cases)
		}
	})

	t.Run("verify without an asker/checker is an error", func(t *testing.T) {
		ev := app.NewEvaluator(querier, nil, nil)
		if _, err := ev.Evaluate(ctx, "docs", []app.EvalCase{{Question: "how does auth work?"}}, 2, true); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})
}
