// Package cache provides a caching decorator around an app.Generator: it serves
// a previously synthesized answer from an app.AnswerCache when the same question
// is asked over the same grounding, skipping the LLM round-trip. It is wired in
// the composition root only when caching is enabled.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Generator wraps an inner app.Generator with a persistent answer cache. The key
// is a hash of the salt (model + prompt identity, supplied by the composition
// root), the question, and the ordered grounding chunks INCLUDING their text —
// because chunk IDs are path-stable (a re-ingested document keeps the same IDs
// with new text), keying on IDs alone would serve stale answers. Requests with
// attachments are never cached (ephemeral, and rarely repeated). Cache errors
// are best-effort: they never fail the answer.
type Generator struct {
	inner app.Generator
	cache app.AnswerCache
	salt  string
	ttl   time.Duration
	now   func() time.Time
}

// compile-time port check
var _ app.Generator = (*Generator)(nil)

// NewGenerator wraps inner so its answers are cached in store. salt scopes the
// cache to a model/prompt identity; ttl bounds how long an entry stays fresh;
// now supplies the clock (time.Now in production, a fake in tests).
func NewGenerator(inner app.Generator, store app.AnswerCache, salt string, ttl time.Duration, now func() time.Time) *Generator {
	return &Generator{inner: inner, cache: store, salt: salt, ttl: ttl, now: now}
}

// Synthesize returns a cached answer when one exists for the same question and
// grounding within the TTL; otherwise it delegates to the inner generator and
// caches the result.
func (g *Generator) Synthesize(ctx context.Context, question string, hits []domain.ChunkHit, attachments []domain.Attachment) (app.Answer, error) {
	if len(attachments) > 0 {
		return g.inner.Synthesize(ctx, question, hits, attachments)
	}

	now := g.now()
	key := g.key(question, hits)
	if ans, ok, err := g.cache.Get(ctx, key, now.Add(-g.ttl)); err == nil && ok {
		return ans, nil
	}

	ans, err := g.inner.Synthesize(ctx, question, hits, attachments)
	if err != nil {
		return ans, err
	}
	// Best-effort: a cache write/prune failure must not fail the answer.
	_ = g.cache.Put(ctx, key, ans, now)
	_ = g.cache.Prune(ctx, now.Add(-g.ttl))
	return ans, nil
}

// key hashes everything that determines the answer: the salt, the question, and
// each grounding hit's ID, text, source, and seq, in retrieval order (order
// affects the prompt, hence the answer).
func (g *Generator) key(question string, hits []domain.ChunkHit) string {
	h := sha256.New()
	write := func(s string) {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0}) // separator so fields can't run together
	}
	write(g.salt)
	write(question)
	for _, hit := range hits {
		write(string(hit.Chunk.ID))
		write(hit.Chunk.Text)
		write(hit.Source)
		write(strconv.Itoa(hit.Chunk.Seq))
	}
	return hex.EncodeToString(h.Sum(nil))
}
