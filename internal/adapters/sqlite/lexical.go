package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// LexicalIndex is the SQLite FTS5-backed LexicalIndex: chunk text is indexed in an
// FTS5 virtual table and ranked by its built-in BM25. The metadata predicate is
// evaluated in Go after the MATCH (the same single-source-of-truth choice the
// vector index makes), so it cannot drift from the memstore reference.
type LexicalIndex struct{ db *sql.DB }

// compile-time port check
var _ app.LexicalIndex = (*LexicalIndex)(nil)

// Upsert replaces each chunk's lexical content. FTS5 has no UPSERT, so each row is
// deleted then inserted within one transaction.
func (x *LexicalIndex) Upsert(ctx context.Context, collection string, docs []app.LexicalDoc) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	for _, d := range docs {
		meta, err := encodeMeta(d.Metadata)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id=?`, string(d.ChunkID)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite: lexical replace: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks_fts(chunk_id, collection, content, metadata) VALUES(?,?,?,?)`,
			string(d.ChunkID), collection, d.Text, meta); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite: lexical insert: %w", err)
		}
	}
	return tx.Commit()
}

// Search returns up to k chunk IDs whose content matches any query term, ranked by
// FTS5 BM25 (best first), filtered to those whose metadata satisfies filter.
func (x *LexicalIndex) Search(ctx context.Context, collection, query string, k int, filter domain.Predicate) ([]domain.ChunkID, error) {
	if k <= 0 {
		return nil, nil
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx,
		`SELECT chunk_id, metadata FROM chunks_fts WHERE collection=? AND content MATCH ? ORDER BY bm25(chunks_fts)`,
		collection, match)
	if err != nil {
		return nil, fmt.Errorf("sqlite: lexical search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ChunkID
	for rows.Next() {
		var id, metaRaw string
		if err := rows.Scan(&id, &metaRaw); err != nil {
			return nil, fmt.Errorf("sqlite: scan lexical: %w", err)
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
		out = append(out, domain.ChunkID(id))
		if len(out) >= k {
			break
		}
	}
	return out, rows.Err()
}

// Delete removes the given chunk IDs from the collection; absent IDs are a no-op.
func (x *LexicalIndex) Delete(ctx context.Context, collection string, ids []domain.ChunkID) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, collection)
	for _, id := range ids {
		args = append(args, string(id))
	}
	q := `DELETE FROM chunks_fts WHERE collection=? AND chunk_id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	if _, err := x.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("sqlite: lexical delete: %w", err)
	}
	return nil
}

// ftsQuery turns free user text into a safe FTS5 MATCH expression: each
// alphanumeric token is double-quoted (neutralizing FTS5 operators and special
// characters) and OR-joined so a chunk matching any term is a candidate. Returns
// "" when the query has no usable terms.
func ftsQuery(query string) string {
	var terms []string
	seen := make(map[string]bool)
	for _, tok := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		terms = append(terms, `"`+tok+`"`)
	}
	return strings.Join(terms, " OR ")
}
