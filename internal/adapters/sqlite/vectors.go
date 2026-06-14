package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// VectorIndex is the SQLite-backed VectorIndex: vectors are stored as BLOBs and
// searched by brute-force cosine in Go.
type VectorIndex struct{ db *sql.DB }

// compile-time port check
var _ app.VectorIndex = (*VectorIndex)(nil)

// Upsert stores the vectors, replacing entries with the same ChunkID.
func (x *VectorIndex) Upsert(ctx context.Context, collection string, entries []app.VectorEntry) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	for _, e := range entries {
		meta, err := encodeMeta(e.Metadata)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vectors(chunk_id, collection, vector, metadata) VALUES(?,?,?,?)
			 ON CONFLICT(chunk_id) DO UPDATE SET collection=excluded.collection, vector=excluded.vector, metadata=excluded.metadata`,
			e.ChunkID, collection, encodeVector(e.Vector), meta); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite: upsert vector: %w", err)
		}
	}
	return tx.Commit()
}

// Search returns up to k matches by cosine similarity, best first, considering
// only rows whose metadata satisfies filter. The predicate is evaluated in Go (in
// the brute-force scan the index already runs), not translated to SQL, so the
// domain evaluator is the single source of truth for filter semantics and
// memstore and sqlite cannot diverge. The zero predicate skips the metadata
// decode entirely.
func (x *VectorIndex) Search(ctx context.Context, collection string, query []float32, k int, filter domain.Predicate) ([]domain.VectorMatch, error) {
	if k <= 0 {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT chunk_id, vector, metadata FROM vectors WHERE collection=?`, collection)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var matches []domain.VectorMatch
	for rows.Next() {
		var (
			id      domain.ChunkID
			raw     []byte
			metaRaw string
		)
		if err := rows.Scan(&id, &raw, &metaRaw); err != nil {
			return nil, fmt.Errorf("sqlite: scan vector: %w", err)
		}
		if !filter.IsZero() {
			meta, err := decodeMeta(metaRaw)
			if err != nil {
				return nil, err
			}
			if !filter.Match(meta) {
				continue
			}
		}
		matches = append(matches, domain.VectorMatch{ChunkID: id, Score: cosine(query, decodeVector(raw))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].ChunkID < matches[j].ChunkID // deterministic ties
	})
	if len(matches) > k {
		matches = matches[:k]
	}
	return matches, nil
}

// Entries returns every stored (chunk_id, vector, metadata) for the collection.
// An unknown collection yields no entries and no error.
func (x *VectorIndex) Entries(ctx context.Context, collection string) ([]app.VectorEntry, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT chunk_id, vector, metadata FROM vectors WHERE collection=?`, collection)
	if err != nil {
		return nil, fmt.Errorf("sqlite: entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []app.VectorEntry
	for rows.Next() {
		var (
			id      domain.ChunkID
			raw     []byte
			metaRaw string
		)
		if err := rows.Scan(&id, &raw, &metaRaw); err != nil {
			return nil, fmt.Errorf("sqlite: scan vector: %w", err)
		}
		meta, err := decodeMeta(metaRaw)
		if err != nil {
			return nil, err
		}
		out = append(out, app.VectorEntry{ChunkID: id, Vector: decodeVector(raw), Metadata: meta})
	}
	return out, rows.Err()
}

// Delete removes the given chunk IDs from the collection; absent IDs are a no-op.
func (x *VectorIndex) Delete(ctx context.Context, collection string, ids []domain.ChunkID) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, collection)
	for _, id := range ids {
		args = append(args, id)
	}
	query := `DELETE FROM vectors WHERE collection=? AND chunk_id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	if _, err := x.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("sqlite: delete vectors: %w", err)
	}
	return nil
}
