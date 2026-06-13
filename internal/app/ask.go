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
// alone. A non-empty source restricts retrieval to documents matching that glob
// (see Querier.Query).
func (a *Asker) Ask(ctx context.Context, collection, question string, k int, attachments []domain.Attachment, strict bool, source string) (Answer, error) {
	hits, err := a.querier.Query(ctx, collection, question, k, source)
	if err != nil {
		return Answer{}, err
	}
	return a.groundAndSynthesize(ctx, collection, question, hits, attachments, strict)
}

// AskExplain is Ask, additionally returning the retrieval (the grounding hits
// plus the runner-up just outside them) so a caller can show which chunks fed
// the answer, at what scores, and whether the cutoff was arbitrary —
// distinguishing retrieval starvation (every score low) from a synthesis miss (a
// high-scoring chunk the model left uncited). Strict/source/attachment semantics
// match Ask. The hits are the post-filter top-k, best first; the Retrieval is
// zero when retrieval found nothing or strict mode returns ErrNoGrounding.
func (a *Asker) AskExplain(ctx context.Context, collection, question string, k int, attachments []domain.Attachment, strict bool, source string) (Answer, Retrieval, error) {
	ret, err := a.querier.Explain(ctx, collection, question, k, source)
	if err != nil {
		return Answer{}, Retrieval{}, err
	}
	ans, err := a.groundAndSynthesize(ctx, collection, question, ret.Hits, attachments, strict)
	if err != nil {
		return Answer{}, Retrieval{}, err
	}
	return ans, ret, nil
}

// groundAndSynthesize enforces the strict grounding guard then synthesizes —
// the shared tail of Ask and AskExplain.
func (a *Asker) groundAndSynthesize(ctx context.Context, collection, question string, hits []domain.ChunkHit, attachments []domain.Attachment, strict bool) (Answer, error) {
	if len(hits) == 0 && len(attachments) == 0 && strict {
		return Answer{}, fmt.Errorf("ask %q: %w: no chunks matched and no attachments supplied", collection, ErrNoGrounding)
	}
	return a.Synthesize(ctx, question, hits, attachments)
}

// Synthesize generates an answer from already-retrieved hits and optional
// attachments, without performing retrieval — the generation half of Ask,
// exposed so callers can interpose between retrieval and synthesis (filter,
// re-rank, or merge hits). Grounded reflects whether any hits or attachments
// were given. Unlike Ask it has no strict mode: the caller chose the hits.
func (a *Asker) Synthesize(ctx context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment) (Answer, error) {
	answer, err := a.generator.Synthesize(ctx, question, hits, attachments)
	if err != nil {
		return Answer{}, fmt.Errorf("synthesize: %w", err)
	}
	answer.Grounded = len(hits) > 0 || len(attachments) > 0
	return answer, nil
}

// SynthesizeStream is Synthesize with incremental delivery: when the underlying
// generator implements StreamingGenerator it emits prose via onDelta as it
// arrives; otherwise it falls back to a single Synthesize and emits the whole
// text in one onDelta call. Either way it returns the complete Answer (with
// Grounded set as Synthesize does), so the caller still gets citations for the
// post-answer sources, --json, and caching.
func (a *Asker) SynthesizeStream(ctx context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment, onDelta func(string)) (Answer, error) {
	var (
		answer Answer
		err    error
	)
	if s, ok := a.generator.(StreamingGenerator); ok {
		answer, err = s.SynthesizeStream(ctx, question, hits, attachments, onDelta)
	} else {
		answer, err = a.generator.Synthesize(ctx, question, hits, attachments)
		if err == nil {
			onDelta(answer.Text)
		}
	}
	if err != nil {
		return Answer{}, fmt.Errorf("synthesize: %w", err)
	}
	answer.Grounded = len(hits) > 0 || len(attachments) > 0
	return answer, nil
}
