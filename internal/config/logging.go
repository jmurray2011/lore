package config

import (
	"io"
	"log/slog"
)

// NewLogger builds a slog.Logger writing to w (stderr in the composition root),
// with a text or JSON handler per cfg.Format at cfg.Level.
func NewLogger(cfg Log, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	var h slog.Handler
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}
