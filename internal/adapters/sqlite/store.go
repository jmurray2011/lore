// Package sqlite is a persistent backend: one SQLite database file holding all
// collections, documents, chunks, and vectors. A single Store exposes the three
// persistence ports through Collections/Documents/Vectors accessors — Go forbids
// one type from having several same-named methods (Upsert, Delete), so the ports
// are separate types sharing the *sql.DB. Vector search is brute-force cosine in
// Go over stored BLOBs. The driver is pure-Go (modernc.org/sqlite): no cgo, so
// the static-binary and cross-compile goals hold.
package sqlite

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/jmurray2011/lore/internal/domain"
	_ "modernc.org/sqlite"
)

var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS collections (
		name TEXT PRIMARY KEY,
		model TEXT NOT NULL,
		dimensions INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		chunker_strategy TEXT NOT NULL DEFAULT '',
		chunker_version INTEGER NOT NULL DEFAULT 0,
		chunker_size INTEGER NOT NULL DEFAULT 0,
		chunker_overlap INTEGER NOT NULL DEFAULT 0,
		chunker_tokenizer TEXT NOT NULL DEFAULT '',
		chunker_context_prefix INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS collection_sources (
		collection TEXT NOT NULL,
		source TEXT NOT NULL,
		PRIMARY KEY (collection, source)
	)`,
	`CREATE TABLE IF NOT EXISTS documents (
		id TEXT PRIMARY KEY,
		collection TEXT NOT NULL,
		source_uri TEXT NOT NULL,
		hash TEXT NOT NULL,
		ingested_at TEXT NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS documents_by_collection ON documents(collection)`,
	`CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY,
		document_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		text TEXT NOT NULL,
		heading_path TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS chunks_by_document ON chunks(document_id)`,
	`CREATE TABLE IF NOT EXISTS vectors (
		chunk_id TEXT PRIMARY KEY,
		collection TEXT NOT NULL,
		vector BLOB NOT NULL,
		metadata TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS vectors_by_collection ON vectors(collection)`,
	// Lexical (BM25) index for hybrid retrieval. A standalone FTS5 table (not
	// external-content) so it satisfies the LexicalIndex port's Upsert/Search/Delete
	// contract in isolation; chunk_id/collection/metadata are stored UNINDEXED
	// (filtered, not tokenized), only content is full-text indexed. Created on open,
	// so an existing database gains an empty index that ingestion fills going
	// forward (pre-existing chunks degrade to vector-only under --hybrid).
	`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(chunk_id UNINDEXED, collection UNINDEXED, content, metadata UNINDEXED)`,
	`CREATE TABLE IF NOT EXISTS answer_cache (
		key TEXT PRIMARY KEY,
		answer TEXT NOT NULL,
		stored_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS answer_cache_by_stored_at ON answer_cache(stored_at)`,
}

// Store is a SQLite-backed persistence engine.
type Store struct {
	db *sql.DB
}

// connectPragmas make concurrent access survivable. Capping the pool to one
// connection (below) serializes writers inside this process, but lore runs as
// several processes at once — an ingest, an interactive query, a resident `lore
// mcp` server — and nothing coordinates them. SQLite installs no busy handler by
// default, so without these a single reader fails every concurrent write
// instantly with SQLITE_BUSY: an ingest loses documents for as long as the
// reader lives, while its own walk keeps going. busy_timeout makes a contended
// write wait instead of dying; WAL means a reader does not block it at all.
const connectPragmas = "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"

// dsn appends the connection pragmas to a database path, respecting a path that
// already carries query parameters.
func dsn(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + connectPragmas
}

// Open opens (creating if needed) the database at path and applies the schema.
// Pass ":memory:" for an ephemeral store. The pool is capped to one connection:
// SQLite is single-writer, and an in-memory database lives on its connection.
// Journal mode is persisted in the database file, so opening an existing one
// migrates it to WAL for every later opener; an in-memory or network-hosted
// database that cannot support WAL simply keeps its mode, still covered by the
// busy timeout.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range schemaStmts {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite: apply schema: %w", err)
		}
	}
	// Migrate databases created before a column was added (CREATE TABLE IF NOT
	// EXISTS leaves an existing table untouched).
	if err := ensureColumn(db, "documents", "fingerprint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "chunks", "heading_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Metadata columns (the --where filter substrate). Pre-metadata rows read back
	// as the '{}' default — an empty Metadata that no condition matches — so an
	// old collection keeps querying, just without metadata to filter on.
	if err := ensureColumn(db, "documents", "metadata", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "vectors", "metadata", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Chunker pin columns. Pre-pin collections get the zero-value defaults, which
	// read back as a zero ChunkerSpec — i.e. unpinned (read-only, not ingestable).
	for _, col := range []struct{ name, def string }{
		{"chunker_strategy", "TEXT NOT NULL DEFAULT ''"},
		{"chunker_version", "INTEGER NOT NULL DEFAULT 0"},
		{"chunker_size", "INTEGER NOT NULL DEFAULT 0"},
		{"chunker_overlap", "INTEGER NOT NULL DEFAULT 0"},
		{"chunker_tokenizer", "TEXT NOT NULL DEFAULT ''"},
		{"chunker_context_prefix", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(db, "collections", col.name, col.def); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// ensureColumn adds column to table with the given definition if it is not
// already present, so an older database is migrated forward in place.
func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("sqlite: inspect %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("sqlite: inspect %s: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("sqlite: add %s.%s: %w", table, column, err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Collections returns the CollectionRepository view of the store.
func (s *Store) Collections() *CollectionRepository { return &CollectionRepository{db: s.db} }

// Documents returns the DocumentRepository view of the store.
func (s *Store) Documents() *DocumentRepository { return &DocumentRepository{db: s.db} }

// Vectors returns the VectorIndex view of the store.
func (s *Store) Vectors() *VectorIndex { return &VectorIndex{db: s.db} }

// Lexical returns the LexicalIndex (FTS5) view of the store.
func (s *Store) Lexical() *LexicalIndex { return &LexicalIndex{db: s.db} }

// Cache returns the AnswerCache view of the store.
func (s *Store) Cache() *AnswerCache { return &AnswerCache{db: s.db} }

func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// encodeMeta serializes metadata to the JSON stored in the metadata column. Nil
// or empty metadata encodes to "{}" (the column default), so a round-trip of an
// empty map and an absent map are indistinguishable — both decode to nil.
func encodeMeta(m domain.Metadata) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]string(m))
	if err != nil {
		return "", fmt.Errorf("sqlite: encode metadata: %w", err)
	}
	return string(b), nil
}

// decodeMeta parses a metadata JSON column value. An empty or "{}" value decodes
// to nil so callers see no metadata rather than an empty map.
func decodeMeta(s string) (domain.Metadata, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m domain.Metadata
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("sqlite: decode metadata: %w", err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// cosine returns the cosine similarity of a and b, 0 for degenerate input
// (mismatched lengths or zero vectors). Matches the memstore reference.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
