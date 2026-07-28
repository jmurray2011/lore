package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// resolveDocURI maps a human-friendly --doc selector to exactly one document's
// source URI, drawn from the collection's documents. It bridges the gap between
// what `lore docs` shows (basenames) and what --doc historically required (the
// full source URI).
//
// Selectors are tried in order of specificity; the first tier that matches any
// document wins:
//
//  1. exact source URI (so existing full-URI callers keep working unchanged),
//  2. exact basename (the label `lore docs` prints),
//  3. glob against the basename (filepath.Match, e.g. "Meeting*"),
//  4. case-insensitive substring of the full URI.
//
// A tier that matches a single document resolves it. A tier that matches more
// than one is ambiguous: the selector must be refined (ErrInvalidArgument, with
// the candidates listed). No tier matching anything is ErrNotFound.
func resolveDocURI(docs []*domain.Document, selector string) (string, error) {
	if selector == "" {
		return "", fmt.Errorf("%w: --doc requires a value", domain.ErrInvalidArgument)
	}

	lower := strings.ToLower(selector)
	tiers := []func(uri string) bool{
		func(uri string) bool { return uri == selector },
		func(uri string) bool { return docBase(uri) == selector },
		func(uri string) bool { ok, err := filepath.Match(selector, docBase(uri)); return err == nil && ok },
		func(uri string) bool { return strings.Contains(strings.ToLower(uri), lower) },
	}

	for _, match := range tiers {
		var hits []string
		for _, d := range docs {
			if match(d.SourceURI) {
				hits = append(hits, d.SourceURI)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return "", ambiguous(selector, hits)
		}
	}
	return "", fmt.Errorf("%w: no document matching %q", app.ErrNotFound, selector)
}

// docBase reduces a source URI to the basename `lore docs` displays, so a
// selector matches what the user sees. It mirrors shortLabel's stripping.
func docBase(uri string) string { return shortLabel(uri) }

// ambiguous builds the error for a selector that matched several documents,
// listing the candidate basenames so the caller can refine.
func ambiguous(selector string, hits []string) error {
	labels := make([]string, len(hits))
	for i, uri := range hits {
		labels[i] = docBase(uri)
	}
	return fmt.Errorf("%w: %q matches %d documents (%s); refine the selector",
		domain.ErrInvalidArgument, selector, len(hits), strings.Join(labels, ", "))
}
