// Package tiktoken is the TokenCounter adapter: it counts tokens with a pure-Go,
// offline tiktoken BPE codec (o200k_base — the encoding text-embedding-3 and
// gpt-4o use). lore is provider-agnostic, so exact counts vary by model; this is
// treated as approximate-but-stable, which is enough to size chunks and bound
// retrieval budgets predictably. The dependency lives only here — the domain
// chunkers receive Count as an injected func, keeping stdlib-only domain clean.
package tiktoken

import (
	"fmt"
	"strings"

	tokenizer "github.com/tiktoken-go/tokenizer"

	"github.com/jmurray2011/lore/internal/app"
)

// EncodingName identifies the tiktoken encoding this adapter counts with. It is
// recorded in a Collection's ChunkerSpec so a change of tokenizer (which alters
// token counts and therefore chunk boundaries) is a chunker mismatch.
const EncodingName = "o200k_base"

// Counter counts tokens with a tiktoken codec. Construct with New; the zero value
// is not usable.
type Counter struct {
	codec tokenizer.Codec
}

// compile-time port check
var _ app.TokenCounter = (*Counter)(nil)

// New returns a Counter over the o200k_base encoding. The codec's vocabulary is
// compiled into the binary, so this never touches the network.
func New() (*Counter, error) {
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return nil, fmt.Errorf("tiktoken: load o200k_base: %w", err)
	}
	return &Counter{codec: codec}, nil
}

// Count returns the number of tokens in text. On the rare tokenize failure it
// falls back to a whitespace-word count, so sizing degrades to a sane estimate
// rather than reporting zero.
func (c *Counter) Count(text string) int {
	n, err := c.codec.Count(text)
	if err != nil {
		return len(strings.Fields(text))
	}
	return n
}
