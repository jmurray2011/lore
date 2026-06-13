package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmurray2011/lore/internal/app"
)

// AnswerCache is the SQLite-backed app.AnswerCache: synthesized answers keyed by
// an opaque content hash, surviving across processes (the point of caching for a
// CLI). The answer is stored as JSON; timestamps are RFC3339Nano text so the
// stored_at index orders correctly.
type AnswerCache struct{ db *sql.DB }

// compile-time port check
var _ app.AnswerCache = (*AnswerCache)(nil)

// Get returns the cached answer for key when present and stored at or after
// notBefore; otherwise ok is false.
func (c *AnswerCache) Get(ctx context.Context, key string, notBefore time.Time) (app.Answer, bool, error) {
	var blob string
	err := c.db.QueryRowContext(ctx,
		`SELECT answer FROM answer_cache WHERE key=? AND stored_at>=?`,
		key, notBefore.UTC().Format(time.RFC3339Nano)).Scan(&blob)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return app.Answer{}, false, nil
	case err != nil:
		return app.Answer{}, false, fmt.Errorf("sqlite: get cached answer: %w", err)
	}
	var ans app.Answer
	if err := json.Unmarshal([]byte(blob), &ans); err != nil {
		return app.Answer{}, false, fmt.Errorf("sqlite: decode cached answer: %w", err)
	}
	return ans, true, nil
}

// Put stores answer under key (replacing any prior entry), stamped storedAt.
func (c *AnswerCache) Put(ctx context.Context, key string, answer app.Answer, storedAt time.Time) error {
	blob, err := json.Marshal(answer)
	if err != nil {
		return fmt.Errorf("sqlite: encode answer: %w", err)
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO answer_cache(key, answer, stored_at) VALUES(?,?,?)
		 ON CONFLICT(key) DO UPDATE SET answer=excluded.answer, stored_at=excluded.stored_at`,
		key, string(blob), storedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("sqlite: put cached answer: %w", err)
	}
	return nil
}

// Prune deletes every entry stored before cutoff.
func (c *AnswerCache) Prune(ctx context.Context, cutoff time.Time) error {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM answer_cache WHERE stored_at<?`, cutoff.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("sqlite: prune answer cache: %w", err)
	}
	return nil
}
