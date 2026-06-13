package tiktoken_test

import (
	"testing"

	"github.com/jmurray2011/lore/internal/adapters/tiktoken"
)

func TestCounter(t *testing.T) {
	c, err := tiktoken.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("known counts (o200k_base)", func(t *testing.T) {
		// Stable BPE counts captured from the o200k_base codec. "tokenization"
		// splitting into 2 tokens proves this is real subword tokenization, not a
		// whitespace-word count.
		cases := []struct {
			in   string
			want int
		}{
			{"", 0},
			{"hello", 1},
			{"hello world", 2},
			{"The quick brown fox.", 5},
			{"tokenization", 2},
			{"a b c d e f g h", 8},
		}
		for _, tc := range cases {
			if got := c.Count(tc.in); got != tc.want {
				t.Errorf("Count(%q) = %d, want %d", tc.in, got, tc.want)
			}
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		const s = "the same input always yields the same token count"
		if a, b := c.Count(s), c.Count(s); a != b {
			t.Errorf("non-deterministic: %d != %d", a, b)
		}
	})

	t.Run("monotonic in length", func(t *testing.T) {
		short := c.Count("alpha beta")
		long := c.Count("alpha beta gamma delta epsilon")
		if long <= short {
			t.Errorf("longer text should not count fewer tokens: short=%d long=%d", short, long)
		}
	})
}
