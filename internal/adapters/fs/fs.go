// Package fs is a filesystem Source: it walks a path (file or directory) and
// yields each regular, non-hidden file as a SourceItem whose content type is
// detected from the extension. Hidden entries (names beginning with ".") are
// skipped so VCS and editor metadata never gets ingested. fs does not filter by
// whether content is supported — the Extractor decides that downstream.
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/limitio"
)

// fingerprintSample bounds the head/tail bytes hashed into a file's fingerprint.
const fingerprintSample = 8192

// maxFileBytes caps a single file read on ingest, so a pathologically large
// file cannot exhaust memory. It is a var, not a const, only so tests can lower
// it.
var maxFileBytes int64 = 256 << 20

// Source walks the filesystem. Its zero value is ready to use.
type Source struct{}

// compile-time port check
var _ app.Source = Source{}

// NewSource returns a filesystem Source.
func NewSource() Source { return Source{} }

// Walk yields every regular, non-hidden file under root, in lexical order. It
// stops and returns the first error from fn, a read error, or ctx cancellation.
func (Source) Walk(ctx context.Context, root string, fn func(app.SourceItem) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && hidden(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if hidden(d.Name()) || !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		fp, err := fingerprint(path, info.Size())
		if err != nil {
			return err
		}
		uri, err := fileURI(path)
		if err != nil {
			return err
		}
		return fn(app.SourceItem{
			URI:         uri,
			ContentType: contentType(path),
			Fingerprint: fp,
			ModTime:     info.ModTime(),
			Open:        func() ([]byte, error) { return readCapped(path, maxFileBytes) },
		})
	})
}

// fingerprint is a cheap source-side signature: the file size plus a hash of its
// head and tail. It changes whenever those change, letting the Ingestor skip
// re-reading unchanged files. A file shorter than two samples is hashed whole
// (so small files are effectively content-checked); the unsampled middle of a
// large file is the only blind spot — the full content hash, computed when a
// file is actually read, stays the idempotency source of truth.
func fingerprint(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	var sz [8]byte
	binary.LittleEndian.PutUint64(sz[:], uint64(size))
	_, _ = h.Write(sz[:])

	if err := hashSample(h, f, fingerprintSample); err != nil {
		return "", err
	}
	if size > int64(2*fingerprintSample) {
		if _, err := f.Seek(size-int64(fingerprintSample), io.SeekStart); err != nil {
			return "", err
		}
		if err := hashSample(h, f, fingerprintSample); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d:%x", size, h.Sum(nil)), nil
}

// hashSample reads up to n bytes from r into h, tolerating a short final read.
func hashSample(h io.Writer, r io.Reader, n int) error {
	buf := make([]byte, n)
	read, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	_, _ = h.Write(buf[:read])
	return nil
}

// readCapped reads the whole file at path, failing if it exceeds max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return limitio.ReadAll(f, max)
}

func hidden(name string) bool { return len(name) > 1 && name[0] == '.' }

func fileURI(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return "file://" + filepath.ToSlash(abs), nil
}

func contentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt", ".text":
		return "text/plain"
	case ".csv":
		// Explicit rather than via mime.TypeByExtension: the OS mime table (the
		// Windows registry, /etc/mime.types) can map .csv to a spreadsheet type,
		// which would route it to the wrong extractor.
		return "text/csv"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		// Literal rather than importing the docx adapter: fs must not depend on
		// other adapters (only cmd/lore wires adapters together).
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}
