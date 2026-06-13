// Package artifact defines lore's portable corpus format: a single, versioned,
// self-contained byte stream holding one collection — its embedding-space and
// chunker pins, metadata, documents, chunks, and vectors — so a collection can
// be exported to a file and reconstructed elsewhere with its invariants intact.
//
// The stream is framed: an 8-byte magic, a 4-byte big-endian format version,
// then a gob-encoded Bundle. Framing the version lets an importer reject a newer
// format with a clear error instead of a gob failure. The wire types are
// deliberately decoupled from the domain types (their own plain structs) so the
// format stays stable across domain refactors; the export/import use cases map
// between the two.
package artifact

import (
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// Magic prefixes every artifact, distinguishing it from random bytes and from
	// an age-encrypted stream (which carries its own header).
	Magic = "LORECORP"
	// FormatVersion is the current frame version. Bump it when the Bundle wire
	// shape changes incompatibly; Read rejects anything newer.
	FormatVersion uint32 = 1
)

var (
	// ErrBadFormat means the stream is not a lore artifact (wrong or short magic).
	ErrBadFormat = errors.New("not a lore artifact")
	// ErrUnsupportedVersion means the artifact's format version is newer than this
	// binary understands.
	ErrUnsupportedVersion = errors.New("unsupported artifact format version")
)

// Bundle is a whole exported collection.
type Bundle struct {
	Collection Collection
	Documents  []Document
}

// Collection carries the collection's identity, metadata, and the pins that must
// travel with it so the reconstructed collection enforces the same invariants. A
// zero Chunker means the source collection predated chunker pinning.
type Collection struct {
	Name       string
	Model      string
	Dimensions int
	CreatedAt  time.Time
	Sources    []string
	Chunker    Chunker
}

// Chunker is the serialized chunker pin (mirrors domain.ChunkerSpec).
type Chunker struct {
	Strategy      string
	Version       int
	Size          int
	Overlap       int
	Tokenizer     string
	ContextPrefix bool
}

// Document is one source document and its chunks (with their vectors), enough to
// reconstruct the document, its chunks, and its index entries on import.
type Document struct {
	SourceURI   string
	Hash        string
	IngestedAt  time.Time
	Fingerprint string
	Chunks      []Chunk
}

// Chunk is one stored chunk with its embedding vector. The chunk and document
// IDs are not stored — they are deterministically re-derived from the (target
// collection, source URI, seq) on import, which keeps the artifact rename-safe.
type Chunk struct {
	Seq         int
	Text        string
	HeadingPath string
	Vector      []float32
}

// Write frames and writes b: the magic, the format version, then a gob-encoded
// Bundle.
func Write(w io.Writer, b Bundle) error {
	if _, err := io.WriteString(w, Magic); err != nil {
		return err
	}
	var ver [4]byte
	binary.BigEndian.PutUint32(ver[:], FormatVersion)
	if _, err := w.Write(ver[:]); err != nil {
		return err
	}
	if err := gob.NewEncoder(w).Encode(b); err != nil {
		return fmt.Errorf("artifact: encode: %w", err)
	}
	return nil
}

// Read verifies the magic and version, then decodes the Bundle. A wrong or short
// magic is ErrBadFormat; a version newer than FormatVersion is
// ErrUnsupportedVersion (nothing is decoded); a decode failure on a well-framed
// stream is a wrapped gob error.
func Read(r io.Reader) (Bundle, error) {
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return Bundle{}, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	if string(magic) != Magic {
		return Bundle{}, ErrBadFormat
	}
	var ver [4]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return Bundle{}, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	if v := binary.BigEndian.Uint32(ver[:]); v > FormatVersion {
		return Bundle{}, fmt.Errorf("%w: artifact is version %d, this lore understands up to %d", ErrUnsupportedVersion, v, FormatVersion)
	}
	var b Bundle
	if err := gob.NewDecoder(r).Decode(&b); err != nil {
		return Bundle{}, fmt.Errorf("artifact: decode: %w", err)
	}
	return b, nil
}
