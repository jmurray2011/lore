package app

import (
	"context"
	"fmt"
)

// Asker answers a question with a synthesis grounded in retrieved chunks.
type Asker struct {
	querier   *Querier
	generator Generator
}

// NewAsker wires an Asker over a Querier and a Generator.
func NewAsker(querier *Querier, generator Generator) *Asker {
	return &Asker{querier: querier, generator: generator}
}

// Ask retrieves the top-k chunks for the question and synthesizes an answer
// grounded in them. Retrieval errors short-circuit before the generator is
// called; generator errors are wrapped.
func (a *Asker) Ask(ctx context.Context, collection, question string, k int) (Answer, error) {
	hits, err := a.querier.Query(ctx, collection, question, k)
	if err != nil {
		return Answer{}, err
	}
	answer, err := a.generator.Synthesize(ctx, question, hits)
	if err != nil {
		return Answer{}, fmt.Errorf("synthesize: %w", err)
	}
	return answer, nil
}
