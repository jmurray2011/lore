package domain_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func mustChunker(t *testing.T, size, overlap int) domain.FixedChunker {
	t.Helper()
	c, err := domain.NewFixedChunker(size, overlap)
	if err != nil {
		t.Fatalf("NewFixedChunker(%d, %d): %v", size, overlap, err)
	}
	return c
}

// words builds "w0 w1 ... w(n-1)".
func words(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "w" + strconv.Itoa(i)
	}
	return strings.Join(parts, " ")
}

func TestNewFixedChunker(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		if _, err := domain.NewFixedChunker(512, 76); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects invalid config", func(t *testing.T) {
		cases := []struct {
			name          string
			size, overlap int
		}{
			{"zero size", 0, 0},
			{"negative size", -1, 0},
			{"negative overlap", 4, -1},
			{"overlap equals size", 4, 4},
			{"overlap exceeds size", 4, 5},
		}
		for _, c := range cases {
			if _, err := domain.NewFixedChunker(c.size, c.overlap); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("%s: want ErrInvalidArgument, got %v", c.name, err)
			}
		}
	})
}

func TestChunkerSplit(t *testing.T) {
	t.Run("empty or whitespace-only yields no chunks", func(t *testing.T) {
		c := mustChunker(t, 4, 1)
		for _, in := range []string{"", "   ", "\n\t  \n"} {
			if got := c.Split(in); len(got) != 0 {
				t.Errorf("Split(%q) = %v, want none", in, got)
			}
		}
	})

	t.Run("input shorter than size is a single chunk", func(t *testing.T) {
		c := mustChunker(t, 4, 1)
		got := c.Split("alpha beta gamma")
		want := []string{"alpha beta gamma"}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("overlapping windows", func(t *testing.T) {
		c := mustChunker(t, 4, 1)
		got := c.Split(words(10))
		want := []string{
			"w0 w1 w2 w3",
			"w3 w4 w5 w6",
			"w6 w7 w8 w9",
		}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("zero overlap partitions without repetition", func(t *testing.T) {
		c := mustChunker(t, 4, 0)
		got := c.Split(words(8))
		want := []string{"w0 w1 w2 w3", "w4 w5 w6 w7"}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("no chunk is empty or exceeds size", func(t *testing.T) {
		c := mustChunker(t, 4, 1)
		for _, n := range []int{1, 2, 3, 4, 5, 7, 9, 13, 100} {
			for _, chunk := range c.Split(words(n)) {
				tokens := strings.Fields(chunk)
				if len(tokens) == 0 {
					t.Errorf("n=%d: empty chunk", n)
				}
				if len(tokens) > 4 {
					t.Errorf("n=%d: chunk exceeds size: %q", n, chunk)
				}
			}
		}
	})

	t.Run("covers every token", func(t *testing.T) {
		c := mustChunker(t, 4, 1)
		const n = 13
		present := make(map[string]bool)
		for _, chunk := range c.Split(words(n)) {
			for _, tok := range strings.Fields(chunk) {
				present[tok] = true
			}
		}
		for i := range n {
			if !present["w"+strconv.Itoa(i)] {
				t.Errorf("token w%d missing from chunks", i)
			}
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		c := mustChunker(t, 4, 1)
		in := words(20)
		if !slices.Equal(c.Split(in), c.Split(in)) {
			t.Error("Split must be deterministic")
		}
	})
}
