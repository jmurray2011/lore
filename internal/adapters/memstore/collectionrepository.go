package memstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// CollectionRepository is a thread-safe, in-memory store of Collection
// aggregates, keyed by name. It owns its data: it stores and returns copies so
// callers cannot alias the stored aggregates.
type CollectionRepository struct {
	mu          sync.RWMutex
	collections map[string]domain.Collection
}

// compile-time port check
var _ app.CollectionRepository = (*CollectionRepository)(nil)

// NewCollectionRepository returns an empty repository.
func NewCollectionRepository() *CollectionRepository {
	return &CollectionRepository{collections: make(map[string]domain.Collection)}
}

// Create stores a copy of c, failing with ErrAlreadyExists if the name is taken.
func (r *CollectionRepository) Create(_ context.Context, c *domain.Collection) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.collections[c.Name]; ok {
		return fmt.Errorf("collection %q: %w", c.Name, app.ErrAlreadyExists)
	}
	r.collections[c.Name] = clone(*c)
	return nil
}

// Get returns a copy of the named collection, or ErrNotFound.
func (r *CollectionRepository) Get(_ context.Context, name string) (*domain.Collection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	c, ok := r.collections[name]
	if !ok {
		return nil, fmt.Errorf("collection %q: %w", name, app.ErrNotFound)
	}
	cp := clone(c)
	return &cp, nil
}

// List returns copies of every collection, in unspecified order.
func (r *CollectionRepository) List(_ context.Context) ([]*domain.Collection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*domain.Collection, 0, len(r.collections))
	for _, c := range r.collections {
		cp := clone(c)
		out = append(out, &cp)
	}
	return out, nil
}

// RecordSource appends source to the collection's Sources, idempotently, or
// fails with ErrNotFound.
func (r *CollectionRepository) RecordSource(_ context.Context, name, source string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.collections[name]
	if !ok {
		return fmt.Errorf("collection %q: %w", name, app.ErrNotFound)
	}
	for _, s := range c.Sources {
		if s == source {
			return nil
		}
	}
	c.Sources = append(append([]string(nil), c.Sources...), source)
	r.collections[name] = c
	return nil
}

// clone returns a deep copy of c so the store never aliases a caller's Sources
// slice (and vice versa).
func clone(c domain.Collection) domain.Collection {
	c.Sources = append([]string(nil), c.Sources...)
	return c
}

// Delete removes the named collection, or fails with ErrNotFound. The
// cross-aggregate cascade (its documents and vectors) is the use case's job.
func (r *CollectionRepository) Delete(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.collections[name]; !ok {
		return fmt.Errorf("collection %q: %w", name, app.ErrNotFound)
	}
	delete(r.collections, name)
	return nil
}
