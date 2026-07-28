package domain

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// Operator is the comparison in a --where Condition.
type Operator string

// The supported operators. Ordered/equality comparisons coerce both sides
// (numeric, then date, then string); ~ is a case-insensitive glob with tag-list
// membership. The grammar is deliberately small — a filter, not a query language.
const (
	OpEqual        Operator = "="
	OpNotEqual     Operator = "!="
	OpLess         Operator = "<"
	OpLessEqual    Operator = "<="
	OpGreater      Operator = ">"
	OpGreaterEqual Operator = ">="
	OpMatch        Operator = "~"
)

// Condition is one metadata comparison: <key> <op> <value>.
type Condition struct {
	Key   string
	Op    Operator
	Value string
}

// Predicate is a conjunction (AND) of metadata Conditions — the parsed form of
// the repeatable --where flag. The zero Predicate has no conditions and matches
// every document, so an absent --where filters nothing.
type Predicate struct {
	Conditions []Condition
}

// IsZero reports whether the predicate has no conditions, i.e. matches every
// document. Adapters use it to skip filtering entirely.
func (p Predicate) IsZero() bool { return len(p.Conditions) == 0 }

// ParseWhere parses repeatable --where clauses into a conjunctive Predicate. Each
// clause is one `key op value` (whitespace around each part is trimmed); the
// operators are =, !=, <, <=, >, >=, and ~. No clauses yields the zero predicate.
// A clause with no operator, an empty key, or an empty value is ErrInvalidArgument.
func ParseWhere(clauses []string) (Predicate, error) {
	var p Predicate
	for _, raw := range clauses {
		c, err := parseCondition(raw)
		if err != nil {
			return Predicate{}, err
		}
		p.Conditions = append(p.Conditions, c)
	}
	return p, nil
}

// parseCondition splits one clause at its first operator. A '!' counts as an
// operator only when it begins "!=", so a lone bang leaves the clause without an
// operator (an error), rather than being mistaken for one.
func parseCondition(raw string) (Condition, error) {
	op, start, width := findOperator(raw)
	if width == 0 {
		return Condition{}, fmt.Errorf("where clause %q: %w: expected key<op>value with op one of = != < <= > >= ~", raw, ErrInvalidArgument)
	}
	key := strings.TrimSpace(raw[:start])
	value := strings.TrimSpace(raw[start+width:])
	if key == "" {
		return Condition{}, fmt.Errorf("where clause %q: %w: empty key", raw, ErrInvalidArgument)
	}
	if value == "" {
		return Condition{}, fmt.Errorf("where clause %q: %w: empty value", raw, ErrInvalidArgument)
	}
	return Condition{Key: key, Op: op, Value: value}, nil
}

// findOperator returns the operator, its start index, and its byte width for the
// first operator in s, or width 0 if none is present.
func findOperator(s string) (Operator, int, int) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			if i+1 < len(s) && s[i+1] == '=' {
				return OpLessEqual, i, 2
			}
			return OpLess, i, 1
		case '>':
			if i+1 < len(s) && s[i+1] == '=' {
				return OpGreaterEqual, i, 2
			}
			return OpGreater, i, 1
		case '!':
			if i+1 < len(s) && s[i+1] == '=' {
				return OpNotEqual, i, 2
			}
			// a lone '!' is not an operator; keep scanning
		case '=':
			return OpEqual, i, 1
		case '~':
			return OpMatch, i, 1
		}
	}
	return "", 0, 0
}

// Match reports whether md satisfies every condition. The zero predicate matches
// any metadata, including nil.
func (p Predicate) Match(md Metadata) bool {
	for _, c := range p.Conditions {
		if !c.match(md) {
			return false
		}
	}
	return true
}

// match evaluates one condition. A missing key excludes the document for every
// operator (including !=): a document that lacks the attribute cannot prove it
// satisfies the filter, so it is left out — the conservative choice for an
// audit-style scoping filter.
func (c Condition) match(md Metadata) bool {
	mv, ok := md[c.Key]
	if !ok {
		return false
	}
	switch c.Op {
	case OpEqual:
		return compareValues(mv, c.Value) == 0
	case OpNotEqual:
		return compareValues(mv, c.Value) != 0
	case OpLess:
		return compareValues(mv, c.Value) < 0
	case OpLessEqual:
		return compareValues(mv, c.Value) <= 0
	case OpGreater:
		return compareValues(mv, c.Value) > 0
	case OpGreaterEqual:
		return compareValues(mv, c.Value) >= 0
	case OpMatch:
		return matchGlob(mv, c.Value)
	default:
		return false
	}
}

// compareValues orders two values with numeric-then-date-then-string coercion,
// returning -1, 0, or 1. Both sides must parse as the same kind for that kind to
// apply; otherwise it falls through to a lexical string comparison.
func compareValues(a, b string) int {
	if af, ok := parseNumber(a); ok {
		if bf, ok := parseNumber(b); ok {
			switch {
			case af < bf:
				return -1
			case af > bf:
				return 1
			default:
				return 0
			}
		}
	}
	if at, ok := parseDate(a); ok {
		if bt, ok := parseDate(b); ok {
			return at.Compare(bt)
		}
	}
	return strings.Compare(a, b)
}

// parseNumber parses a decimal value, reporting whether it succeeded.
func parseNumber(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

// parseDate parses a date in any accepted layout — full RFC3339, a zoneless
// datetime, or a date-only value (treated as midnight UTC) — reporting whether it
// succeeded. Shared by --where comparisons and recency inference so both accept
// the same formats.
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// matchGlob reports whether pattern (a case-insensitive glob, path.Match syntax)
// matches the whole value or any comma-separated element of it (tag membership).
// A malformed pattern matches nothing.
func matchGlob(value, pattern string) bool {
	lp := strings.ToLower(pattern)
	if globMatch(lp, strings.ToLower(value)) {
		return true
	}
	for _, el := range strings.Split(value, ",") {
		if globMatch(lp, strings.ToLower(strings.TrimSpace(el))) {
			return true
		}
	}
	return false
}

func globMatch(pattern, s string) bool {
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}
