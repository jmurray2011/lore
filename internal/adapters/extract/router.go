package extract

import (
	"fmt"

	"github.com/jmurray2011/lore/internal/app"
)

// Router dispatches extraction to the first of its Extractors that supports a
// content type, presenting many format adapters as the single Extractor port the
// use cases consume. The composition root builds it from the concrete adapters
// (text, docx, pdf, ...).
type Router struct {
	extractors []app.Extractor
}

// compile-time port check
var _ app.Extractor = (*Router)(nil)

// NewRouter returns a Router over the given Extractors, tried in order.
func NewRouter(extractors ...app.Extractor) *Router {
	return &Router{extractors: extractors}
}

// Supports reports whether any underlying Extractor supports the content type.
func (r *Router) Supports(contentType string) bool {
	return r.find(contentType) != nil
}

// Extract dispatches to the first Extractor that supports the content type, or
// fails if none does.
func (r *Router) Extract(contentType string, raw []byte) (string, error) {
	e := r.find(contentType)
	if e == nil {
		return "", fmt.Errorf("extract: no extractor for content type %q", contentType)
	}
	return e.Extract(contentType, raw)
}

func (r *Router) find(contentType string) app.Extractor {
	for _, e := range r.extractors {
		if e.Supports(contentType) {
			return e
		}
	}
	return nil
}
