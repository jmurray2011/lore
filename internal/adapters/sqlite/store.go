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
	"fmt"
	"math"

	_ "modernc.org/sqlite"
)

var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS collections (
		name TEXT PRIMARY KEY,
		model TEXT NOT NULL,
		dimensions INTEGER NOT NULL,
		created_at TEXT NOT NULL
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
		ingested_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS documents_by_collection ON documents(collection)`,
	`CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY,
		document_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		text TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS chunks_by_document ON chunks(document_id)`,
	`CREATE TABLE IF NOT EXISTS vectors (
		chunk_id TEXT PRIMARY KEY,
		collection TEXT NOT NULL,
		vector BLOB NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS vectors_by_collection ON vectors(collection)`,
}

// Store is a SQLite-backed persistence engine.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
// Pass ":memory:" for an ephemeral store. The pool is capped to one connection:
// SQLite is single-writer, and an in-memory database lives on its connection.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
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
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Collections returns the CollectionRepository view of the store.
func (s *Store) Collections() *CollectionRepository { return &CollectionRepository{db: s.db} }

// Documents returns the DocumentRepository view of the store.
func (s *Store) Documents() *DocumentRepository { return &DocumentRepository{db: s.db} }

// Vectors returns the VectorIndex view of the store.
func (s *Store) Vectors() *VectorIndex { return &VectorIndex{db: s.db} }

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
