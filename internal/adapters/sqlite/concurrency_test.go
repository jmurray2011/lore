package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jmurray2011/lore/internal/adapters/sqlite"
	"github.com/jmurray2011/lore/internal/domain"
)

// openReader returns a second connection to the same database file, standing in
// for another lore process holding the file open: an interactive `lore docs`, or
// one of the resident `lore mcp` servers. It deliberately opens with a bare path,
// the way any older binary would.
func openReader(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestOpenAllowsWritesWhileAnotherConnectionReads pins the property that an
// ingest batch cannot be killed by a concurrent reader. Without a busy timeout
// and WAL journalling, a single reader holding an open read transaction fails
// every write with SQLITE_BUSY immediately, which loses documents for as long as
// the reader lives while the writing command still walks its whole source.
func TestOpenAllowsWritesWhileAnotherConnectionReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lore.db")

	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A read transaction holds its lock until commit or rollback, so this models
	// a reader that is still open when the write lands.
	reader := openReader(t, path)
	tx, err := reader.Begin()
	if err != nil {
		t.Fatalf("reader begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM collections`).Scan(&n); err != nil {
		t.Fatalf("reader query: %v", err)
	}

	space, err := domain.NewEmbeddingSpace("text-embedding-3-small", 1536)
	if err != nil {
		t.Fatalf("NewEmbeddingSpace: %v", err)
	}
	spec, err := domain.NewChunkerSpec("structure", 2, 512, 64, "o200k_base", true)
	if err != nil {
		t.Fatalf("NewChunkerSpec: %v", err)
	}
	coll, err := domain.NewCollection("docs", space, spec, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewCollection: %v", err)
	}

	if err := store.Collections().Create(ctx, coll); err != nil {
		t.Fatalf("write while another connection holds a read: %v", err)
	}
}

// TestOpenEnablesWALJournalMode pins the journal mode itself, because it is
// written into the database file rather than scoped to the connection: every
// later opener inherits it.
func TestOpenEnablesWALJournalMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lore.db")

	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var mode string
	if err := openReader(t, path).QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want %q", mode, "wal")
	}
}
