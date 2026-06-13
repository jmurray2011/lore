package app

import (
	"context"
	"fmt"

	"github.com/jmurray2011/lore/internal/domain"
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
// grounded in them, plus any attachments passed straight to the generator.
// Retrieval errors short-circuit before the generator is called; generator
// errors are wrapped. With k <= 0 no chunks are retrieved, so attachments alone
// ground the answer.
//
// When retrieval yields no chunks and no attachments were supplied there is
// nothing to ground on: in strict mode this is ErrNoGrounding (the generator is
// not called, saving the request); otherwise the answer is still produced but
// marked Grounded=false so the caller can warn that it rests on model knowledge
// alone.
func (a *Asker) Ask(ctx context.Context, collection, question string, k int, attachments []domain.Attachment, strict bool) (Answer, error) {
	hits, err := a.querier.Query(ctx, collection, question, k)
	if err != nil {
		return Answer{}, err
	}

	grounded := len(hits) > 0 || len(attachments) > 0
	if !grounded && strict {
		return Answer{}, fmt.Errorf("ask %q: %w: no chunks matched and no attachments supplied", collection, ErrNoGrounding)
	}

	answer, err := a.generator.Synthesize(ctx, question, hits, attachments)
	if err != nil {
		return Answer{}, fmt.Errorf("synthesize: %w", err)
	}
	answer.Grounded = grounded
	return answer, nil
}
