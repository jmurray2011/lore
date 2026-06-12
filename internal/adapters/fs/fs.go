// Package fs is a filesystem Source: it walks a path (file or directory) and
// yields each regular, non-hidden file as a SourceItem whose content type is
// detected from the extension. Hidden entries (names beginning with ".") are
// skipped so VCS and editor metadata never gets ingested. fs does not filter by
// whether content is supported — the Extractor decides that downstream.
package fs

import (
	"context"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
)

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

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		uri, err := fileURI(path)
		if err != nil {
			return err
		}
		return fn(app.SourceItem{URI: uri, ContentType: contentType(path), Content: content})
	})
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
	default:
		if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}
