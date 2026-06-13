package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// AnswerCache is a thread-safe, in-memory app.AnswerCache (the reference impl,
// and what the "memory" storage backend uses). It is process-local, so it only
// helps within a single run.
type AnswerCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	answer   app.Answer
	storedAt time.Time
}

// compile-time port check
var _ app.AnswerCache = (*AnswerCache)(nil)

// NewAnswerCache returns an empty cache.
func NewAnswerCache() *AnswerCache {
	return &AnswerCache{entries: make(map[string]cacheEntry)}
}

// Get returns the cached answer for key when present and stored at or after
// notBefore; otherwise ok is false.
func (c *AnswerCache) Get(_ context.Context, key string, notBefore time.Time) (app.Answer, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || e.storedAt.Before(notBefore) {
		return app.Answer{}, false, nil
	}
	return e.answer, true, nil
}

// Put stores answer under key (replacing any prior entry), stamped storedAt.
func (c *AnswerCache) Put(_ context.Context, key string, answer app.Answer, storedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Copy the citations so a later mutation of the caller's slice can't corrupt
	// the stored entry.
	if answer.Citations != nil {
		cites := make([]domain.Citation, len(answer.Citations))
		copy(cites, answer.Citations)
		answer.Citations = cites
	}
	c.entries[key] = cacheEntry{answer: answer, storedAt: storedAt}
	return nil
}

// Prune deletes every entry stored before cutoff.
func (c *AnswerCache) Prune(_ context.Context, cutoff time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if e.storedAt.Before(cutoff) {
			delete(c.entries, k)
		}
	}
	return nil
}
