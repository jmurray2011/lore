package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// DocumentRepository is the SQLite-backed DocumentRepository.
type DocumentRepository struct{ db *sql.DB }

// compile-time port check
var _ app.DocumentRepository = (*DocumentRepository)(nil)

// Upsert stores the document and replaces its chunks, atomically.
func (r *DocumentRepository) Upsert(ctx context.Context, doc *domain.Document, chunks []domain.Chunk) error {
	return r.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO documents(id, collection, source_uri, hash, ingested_at, fingerprint) VALUES(?,?,?,?,?,?)
			 ON CONFLICT(id) DO UPDATE SET
			   collection=excluded.collection, source_uri=excluded.source_uri,
			   hash=excluded.hash, ingested_at=excluded.ingested_at, fingerprint=excluded.fingerprint`,
			doc.ID, doc.Collection, doc.SourceURI, doc.Hash, doc.IngestedAt.UTC().Format(time.RFC3339Nano), doc.Fingerprint); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id=?`, doc.ID); err != nil {
			return err
		}
		for _, c := range chunks {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO chunks(id, document_id, seq, text) VALUES(?,?,?,?)`,
				c.ID, c.DocumentID, c.Seq, c.Text); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetBySource returns the document for (collection, sourceURI), or ErrNotFound.
func (r *DocumentRepository) GetBySource(ctx context.Context, collection, sourceURI string) (*domain.Document, error) {
	id := domain.DeriveDocumentID(collection, sourceURI)
	row := r.db.QueryRowContext(ctx,
		`SELECT `+docColumns+` FROM documents WHERE id=? AND collection=?`, id, collection)
	d, err := scanDoc(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("document %q in collection %q: %w", sourceURI, collection, app.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("sqlite: get document: %w", err)
	}
	return d, nil
}

// GetChunks hydrates chunks by ID, preserving input order and skipping IDs with
// no stored chunk.
func (r *DocumentRepository) GetChunks(ctx context.Context, ids []domain.ChunkID) ([]domain.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT id, document_id, seq, text FROM chunks WHERE id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byID := make(map[domain.ChunkID]domain.Chunk, len(ids))
	for rows.Next() {
		var c domain.Chunk
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Seq, &c.Text); err != nil {
			return nil, fmt.Errorf("sqlite: scan chunk: %w", err)
		}
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]domain.Chunk, 0, len(ids))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// GetDocuments hydrates documents by ID, preserving input order and skipping IDs
// with no stored document.
func (r *DocumentRepository) GetDocuments(ctx context.Context, ids []domain.DocumentID) ([]*domain.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT ` + docColumns + ` FROM documents WHERE id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get documents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byID := make(map[domain.DocumentID]*domain.Document, len(ids))
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan document: %w", err)
		}
		byID[d.ID] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// ListDocuments returns every document in the collection, ordered by source URI.
// An unknown or empty collection yields no documents and no error.
func (r *DocumentRepository) ListDocuments(ctx context.Context, collection string) ([]*domain.Document, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+docColumns+` FROM documents WHERE collection=? ORDER BY source_uri`, collection)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list documents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*domain.Document
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan document: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// docColumns is the document column list, in the order scanDoc expects.
const docColumns = "id, collection, source_uri, hash, ingested_at, fingerprint"

// scanDoc reads one document row (docColumns order), parsing the stored
// RFC3339Nano timestamp. It returns the raw Scan error (e.g. sql.ErrNoRows)
// unwrapped so callers can match it.
func scanDoc(s interface{ Scan(...any) error }) (*domain.Document, error) {
	var (
		d          domain.Document
		ingestedAt string
	)
	if err := s.Scan(&d.ID, &d.Collection, &d.SourceURI, &d.Hash, &ingestedAt, &d.Fingerprint); err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, ingestedAt)
	if err != nil {
		return nil, fmt.Errorf("sqlite: parse ingested_at: %w", err)
	}
	d.IngestedAt = t
	return &d, nil
}

// Delete removes the document and its chunks, returning the removed chunk IDs,
// or fails with ErrNotFound.
func (r *DocumentRepository) Delete(ctx context.Context, collection string, id domain.DocumentID) ([]domain.ChunkID, error) {
	var ids []domain.ChunkID
	err := r.tx(ctx, func(tx *sql.Tx) error {
		var exists string
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM documents WHERE id=? AND collection=?`, id, collection).Scan(&exists); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("document %q in collection %q: %w", id, collection, app.ErrNotFound)
		case err != nil:
			return err
		}
		got, err := selectChunkIDs(ctx, tx, `SELECT id FROM chunks WHERE document_id=?`, id)
		if err != nil {
			return err
		}
		ids = got
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id=?`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM documents WHERE id=?`, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteCollection removes every document and its chunks in the collection,
// returning all removed chunk IDs. An empty collection is a no-op.
func (r *DocumentRepository) DeleteCollection(ctx context.Context, collection string) ([]domain.ChunkID, error) {
	var ids []domain.ChunkID
	err := r.tx(ctx, func(tx *sql.Tx) error {
		got, err := selectChunkIDs(ctx, tx,
			`SELECT c.id FROM chunks c JOIN documents d ON c.document_id=d.id WHERE d.collection=?`, collection)
		if err != nil {
			return err
		}
		ids = got
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chunks WHERE document_id IN (SELECT id FROM documents WHERE collection=?)`, collection); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM documents WHERE collection=?`, collection)
		return err
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// selectChunkIDs reads a single-column chunk-ID query to completion, closing its
// rows before the caller issues further statements on the same connection.
func selectChunkIDs(ctx context.Context, tx *sql.Tx, query string, arg any) ([]domain.ChunkID, error) {
	rows, err := tx.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []domain.ChunkID
	for rows.Next() {
		var id domain.ChunkID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// tx runs fn in a transaction, committing on success and rolling back on error.
func (r *DocumentRepository) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
