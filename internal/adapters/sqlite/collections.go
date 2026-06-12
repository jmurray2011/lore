package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// CollectionRepository is the SQLite-backed CollectionRepository.
type CollectionRepository struct{ db *sql.DB }

// compile-time port check
var _ app.CollectionRepository = (*CollectionRepository)(nil)

// Create inserts the collection, failing with ErrAlreadyExists if the name is
// taken. The INSERT ... ON CONFLICT DO NOTHING + RowsAffected check is atomic.
func (r *CollectionRepository) Create(ctx context.Context, c *domain.Collection) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO collections(name, model, dimensions, created_at) VALUES(?,?,?,?) ON CONFLICT(name) DO NOTHING`,
		c.Name, c.Space.Model, c.Space.Dimensions, c.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("sqlite: create collection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: create collection: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("collection %q: %w", c.Name, app.ErrAlreadyExists)
	}
	return nil
}

// Get returns the named collection (with its recorded sources), or ErrNotFound.
func (r *CollectionRepository) Get(ctx context.Context, name string) (*domain.Collection, error) {
	row := r.db.QueryRowContext(ctx, `SELECT name, model, dimensions, created_at FROM collections WHERE name=?`, name)
	c, err := scanCollection(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("collection %q: %w", name, app.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("sqlite: get collection: %w", err)
	}
	if err := r.loadSources(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// List returns every collection (with its recorded sources), in unspecified order.
func (r *CollectionRepository) List(ctx context.Context) ([]*domain.Collection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT name, model, dimensions, created_at FROM collections`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list collections: %w", err)
	}
	var out []*domain.Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite: scan collection: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// Close before issuing the per-collection source queries: one connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, c := range out {
		if err := r.loadSources(ctx, c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Delete removes the collection record and its recorded sources, or fails with
// ErrNotFound. The cross-aggregate cascade (documents and vectors) is the use
// case's job.
func (r *CollectionRepository) Delete(ctx context.Context, name string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE name=?`, name)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlite: delete collection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlite: delete collection: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return fmt.Errorf("collection %q: %w", name, app.ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM collection_sources WHERE collection=?`, name); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlite: delete collection sources: %w", err)
	}
	return tx.Commit()
}

// RecordSource adds source to the collection's recorded sources (idempotent via
// the composite primary key), or fails with ErrNotFound if no such collection.
func (r *CollectionRepository) RecordSource(ctx context.Context, name, source string) error {
	var exists string
	switch err := r.db.QueryRowContext(ctx, `SELECT name FROM collections WHERE name=?`, name).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("collection %q: %w", name, app.ErrNotFound)
	case err != nil:
		return fmt.Errorf("sqlite: record source: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO collection_sources(collection, source) VALUES(?,?) ON CONFLICT DO NOTHING`, name, source); err != nil {
		return fmt.Errorf("sqlite: record source: %w", err)
	}
	return nil
}

// loadSources populates c.Sources from the collection_sources table.
func (r *CollectionRepository) loadSources(ctx context.Context, c *domain.Collection) error {
	rows, err := r.db.QueryContext(ctx, `SELECT source FROM collection_sources WHERE collection=? ORDER BY source`, c.Name)
	if err != nil {
		return fmt.Errorf("sqlite: load sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return fmt.Errorf("sqlite: scan source: %w", err)
		}
		c.Sources = append(c.Sources, s)
	}
	return rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows so one scan helper serves Get + List.
type scanner interface{ Scan(dest ...any) error }

func scanCollection(s scanner) (*domain.Collection, error) {
	var (
		name, model, createdAt string
		dims                   int
	)
	if err := s.Scan(&name, &model, &dims, &createdAt); err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	return &domain.Collection{
		Name:      name,
		Space:     domain.EmbeddingSpace{Model: model, Dimensions: dims},
		CreatedAt: t,
	}, nil
}
