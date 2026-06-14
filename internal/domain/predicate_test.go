package domain_test

import (
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestParseWhere(t *testing.T) {
	t.Run("empty clauses yield the zero predicate", func(t *testing.T) {
		p, err := domain.ParseWhere(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !p.IsZero() {
			t.Errorf("no clauses should be the zero predicate, got %+v", p)
		}
	})

	t.Run("parses each operator, trimming whitespace", func(t *testing.T) {
		cases := []struct {
			clause string
			want   domain.Condition
		}{
			{"author=alice", domain.Condition{Key: "author", Op: domain.OpEqual, Value: "alice"}},
			{"author != bob", domain.Condition{Key: "author", Op: domain.OpNotEqual, Value: "bob"}},
			{"size < 1000", domain.Condition{Key: "size", Op: domain.OpLess, Value: "1000"}},
			{"size<=1000", domain.Condition{Key: "size", Op: domain.OpLessEqual, Value: "1000"}},
			{"date > 2025-01-01", domain.Condition{Key: "date", Op: domain.OpGreater, Value: "2025-01-01"}},
			{"date >= 2025-01-01", domain.Condition{Key: "date", Op: domain.OpGreaterEqual, Value: "2025-01-01"}},
			{"tags ~ security", domain.Condition{Key: "tags", Op: domain.OpMatch, Value: "security"}},
		}
		for _, tc := range cases {
			p, err := domain.ParseWhere([]string{tc.clause})
			if err != nil {
				t.Fatalf("ParseWhere(%q): %v", tc.clause, err)
			}
			if len(p.Conditions) != 1 || p.Conditions[0] != tc.want {
				t.Errorf("ParseWhere(%q) = %+v, want [%+v]", tc.clause, p.Conditions, tc.want)
			}
		}
	})

	t.Run("multiple clauses become a conjunction", func(t *testing.T) {
		p, err := domain.ParseWhere([]string{"author=alice", "date>=2025-01-01"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.Conditions) != 2 {
			t.Fatalf("want 2 conditions, got %d", len(p.Conditions))
		}
	})

	t.Run("rejects malformed clauses", func(t *testing.T) {
		bad := []string{
			"",        // empty
			"   ",     // blank
			"author",  // no operator
			"=alice",  // empty key
			"author=", // empty value
			"author!", // lone bang is not an operator
			"  = ",    // empty key and value
		}
		for _, clause := range bad {
			if _, err := domain.ParseWhere([]string{clause}); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("ParseWhere(%q): want ErrInvalidArgument, got %v", clause, err)
			}
		}
	})
}

func TestPredicateMatch(t *testing.T) {
	md := domain.Metadata{
		"author": "alice",
		"size":   "1500",
		"date":   "2025-06-01",
		"tags":   "security,compliance",
		"title":  "Q3 Incident Report",
	}

	cases := []struct {
		name   string
		clause string
		want   bool
	}{
		// equality, string
		{"eq string match", "author=alice", true},
		{"eq string miss", "author=bob", false},
		{"ne string match", "author!=bob", true},
		{"ne string miss", "author!=alice", false},
		// equality, numeric coercion
		{"eq numeric coercion", "size=1500.0", true},
		// ordered, numeric
		{"gt numeric true", "size>1000", true},
		{"gt numeric false", "size>2000", false},
		{"le numeric true", "size<=1500", true},
		// ordered, date
		{"date ge true", "date>=2025-01-01", true},
		{"date lt false", "date<2025-01-01", false},
		{"date le boundary", "date<=2025-06-01", true},
		// match: glob, tag membership, case-insensitive
		{"match tag membership", "tags~security", true},
		{"match tag membership second", "tags~compliance", true},
		{"match tag absent", "tags~privacy", false},
		{"match glob", "title~*Report*", true},
		{"match case-insensitive", "title~*report*", true},
		// missing key excludes for every operator, including !=
		{"missing key eq", "owner=alice", false},
		{"missing key ne", "owner!=alice", false},
		{"missing key gt", "owner>1", false},
		{"missing key match", "owner~x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := domain.ParseWhere([]string{tc.clause})
			if err != nil {
				t.Fatalf("ParseWhere(%q): %v", tc.clause, err)
			}
			if got := p.Match(md); got != tc.want {
				t.Errorf("%q.Match(md) = %v, want %v", tc.clause, got, tc.want)
			}
		})
	}

	t.Run("zero predicate matches everything", func(t *testing.T) {
		var p domain.Predicate
		if !p.Match(md) || !p.Match(nil) {
			t.Error("the zero predicate must match all metadata, including nil")
		}
	})

	t.Run("conjunction requires every condition", func(t *testing.T) {
		both, _ := domain.ParseWhere([]string{"author=alice", "size>1000"})
		if !both.Match(md) {
			t.Error("both conditions hold, predicate should match")
		}
		oneFails, _ := domain.ParseWhere([]string{"author=alice", "size>9000"})
		if oneFails.Match(md) {
			t.Error("one condition fails, predicate must not match")
		}
	})
}
