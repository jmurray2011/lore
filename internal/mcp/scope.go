package mcp

import (
	"fmt"
	"path"

	"github.com/jmurray2011/lore/internal/domain"
)

// scope restricts which collections the server exposes. An empty pattern set
// means every local collection is in scope; otherwise a collection is in scope
// iff its name matches one of the patterns exactly or as a glob (path.Match).
// It backs the `--collections` server flag: an out-of-scope collection is absent
// from list_collections and a tool error for every other tool.
type scope struct {
	patterns []string
}

// newScope validates the patterns (a malformed glob is a usage error) and
// returns the scope. No patterns ⇒ everything is in scope.
func newScope(patterns []string) (scope, error) {
	for _, p := range patterns {
		// path.Match only reports ErrBadPattern for a malformed pattern, regardless
		// of the name, so matching against "" surfaces it without a real name.
		if _, err := path.Match(p, ""); err != nil {
			return scope{}, fmt.Errorf("%w: invalid collection pattern %q: %v", domain.ErrInvalidArgument, p, err)
		}
	}
	return scope{patterns: patterns}, nil
}

// all reports whether the scope imposes no restriction.
func (s scope) all() bool { return len(s.patterns) == 0 }

// allows reports whether name is exposed by this server.
func (s scope) allows(name string) bool {
	if s.all() {
		return true
	}
	for _, p := range s.patterns {
		if p == name {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// check returns a tool error if any requested collection is out of scope. The
// message deliberately does not confirm whether the collection exists — an
// out-of-scope name is simply unavailable on this server.
func (s scope) check(names ...string) error {
	for _, n := range names {
		if !s.allows(n) {
			return fmt.Errorf("collection %q is not available on this MCP server", n)
		}
	}
	return nil
}
