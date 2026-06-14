// Package limitio provides bounded reads that fail loudly when an input exceeds
// a byte cap. It exists because the stdlib's [io.LimitReader] silently returns
// io.EOF at its limit — indistinguishable from a genuinely short input — which
// is the wrong behavior when the goal is to reject oversized untrusted data
// (decompression bombs, attacker-supplied artifacts) rather than truncate it.
//
// The cap is inclusive: exactly max bytes is allowed; the first byte past max
// surfaces [ErrTooLarge].
package limitio

import (
	"errors"
	"io"
)

// ErrTooLarge is returned when a bounded read would exceed its cap.
var ErrTooLarge = errors.New("limitio: input exceeds maximum allowed size")

// Reader wraps r so that reading more than max bytes in total fails with
// [ErrTooLarge] instead of silently stopping. A source that ends at or before
// max passes through its normal io.EOF.
func Reader(r io.Reader, max int64) io.Reader {
	return &limitReader{r: r, remaining: max}
}

// ReadAll reads all of r into memory, failing with [ErrTooLarge] if r yields
// more than max bytes. It is the bounded counterpart to [io.ReadAll].
func ReadAll(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(Reader(r, max))
}

type limitReader struct {
	r         io.Reader
	remaining int64 // bytes still permitted before the cap is exceeded
}

func (lr *limitReader) Read(p []byte) (int, error) {
	if lr.remaining <= 0 {
		// The cap is used up. Probe one byte: if the source still has data the
		// input is over the cap; if it's exhausted this is a clean EOF.
		var b [1]byte
		if n, _ := lr.r.Read(b[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > lr.remaining {
		p = p[:lr.remaining]
	}
	n, err := lr.r.Read(p)
	lr.remaining -= int64(n)
	return n, err
}
