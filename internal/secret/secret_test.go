package secret_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/jmurray2011/lore/internal/secret"
)

func TestFromCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test commands assume a POSIX shell")
	}
	ctx := context.Background()

	t.Run("reads stdout", func(t *testing.T) {
		got, err := secret.FromCommand(ctx, "printf '%s' hunter2")
		if err != nil || got != "hunter2" {
			t.Errorf("got %q, %v; want hunter2, nil", got, err)
		}
	})

	t.Run("trims the trailing newline", func(t *testing.T) {
		got, err := secret.FromCommand(ctx, "echo hunter2")
		if err != nil || got != "hunter2" {
			t.Errorf("got %q, %v; want hunter2, nil", got, err)
		}
	})

	t.Run("non-zero exit is an error", func(t *testing.T) {
		if _, err := secret.FromCommand(ctx, "exit 3"); err == nil {
			t.Error("want error for a failing command")
		}
	})

	t.Run("empty output is an error", func(t *testing.T) {
		if _, err := secret.FromCommand(ctx, "true"); err == nil {
			t.Error("want error when the command produces nothing")
		}
	})

	t.Run("empty command is an error", func(t *testing.T) {
		if _, err := secret.FromCommand(ctx, "   "); err == nil {
			t.Error("want error for an empty command")
		}
	})
}
