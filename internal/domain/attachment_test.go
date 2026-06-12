package domain_test

import (
	"errors"
	"testing"

	"github.com/jmurray2011/lore/internal/domain"
)

func TestNewAttachment(t *testing.T) {
	t.Run("valid attachment", func(t *testing.T) {
		a, err := domain.NewAttachment("image/png", "chart.png", []byte{1, 2, 3})
		if err != nil {
			t.Fatalf("NewAttachment: %v", err)
		}
		if a.MediaType != "image/png" || a.Name != "chart.png" || len(a.Data) != 3 {
			t.Errorf("attachment = %+v", a)
		}
	})

	t.Run("name is optional", func(t *testing.T) {
		if _, err := domain.NewAttachment("application/pdf", "", []byte{1}); err != nil {
			t.Errorf("empty name should be allowed: %v", err)
		}
	})

	t.Run("empty media type is invalid", func(t *testing.T) {
		if _, err := domain.NewAttachment("  ", "x", []byte{1}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("empty data is invalid", func(t *testing.T) {
		if _, err := domain.NewAttachment("image/png", "x", nil); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("want ErrInvalidArgument, got %v", err)
		}
	})
}
