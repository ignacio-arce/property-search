// Package logging builds the bot's slog logger. It exists so the handler and its
// level live in one place, and so tests have a sink without hand-rolling one.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger that writes human-readable text to w at the given level.
// The TextHandler prints time with milliseconds, so per-operation timings can be
// read off the same line without a second format.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// Discard returns a logger that writes nowhere. Tests use it in place of a real
// sink; it is always non-nil and safe to call.
func Discard() *slog.Logger {
	return New(io.Discard, slog.LevelInfo)
}
