package limitio_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/limitio"
)

func TestReadAll(t *testing.T) {
	t.Run("under the cap reads fully", func(t *testing.T) {
		got, err := limitio.ReadAll(strings.NewReader("hello"), 5)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("exactly at the cap reads fully", func(t *testing.T) {
		got, err := limitio.ReadAll(strings.NewReader("hello"), 5)
		if err != nil {
			t.Fatalf("ReadAll at cap: %v", err)
		}
		if string(got) != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("one past the cap is ErrTooLarge", func(t *testing.T) {
		_, err := limitio.ReadAll(strings.NewReader("hello!"), 5)
		if !errors.Is(err, limitio.ErrTooLarge) {
			t.Fatalf("want ErrTooLarge, got %v", err)
		}
	})

	t.Run("far past the cap is ErrTooLarge", func(t *testing.T) {
		_, err := limitio.ReadAll(bytes.NewReader(make([]byte, 1<<20)), 1024)
		if !errors.Is(err, limitio.ErrTooLarge) {
			t.Fatalf("want ErrTooLarge, got %v", err)
		}
	})
}

func TestReader(t *testing.T) {
	t.Run("streams data and surfaces ErrTooLarge past the cap", func(t *testing.T) {
		// A reader that hands back one byte per Read exercises the running tally
		// across many calls, not just a single oversized read.
		r := limitio.Reader(&iotest{src: []byte("0123456789")}, 4)
		_, err := io.ReadAll(r)
		if !errors.Is(err, limitio.ErrTooLarge) {
			t.Fatalf("want ErrTooLarge, got %v", err)
		}
	})

	t.Run("a source that ends exactly at the cap yields EOF, not an error", func(t *testing.T) {
		r := limitio.Reader(&iotest{src: []byte("abcd")}, 4)
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != "abcd" {
			t.Errorf("got %q, want %q", got, "abcd")
		}
	})
}

// iotest is a deliberately pathological reader that returns a single byte per
// Read, so tests cover the cap being crossed across multiple reads.
type iotest struct {
	src []byte
	pos int
}

func (r *iotest) Read(p []byte) (int, error) {
	if r.pos >= len(r.src) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.src[r.pos]
	r.pos++
	return 1, nil
}
