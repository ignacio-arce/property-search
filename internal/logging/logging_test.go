package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The handler must stay human-readable and carry the time with milliseconds, so
// per-operation timings can be read off the same line.
func TestNewEmitsReadableText(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	l.Info("digest: user done", "user", 42, "sent", 3)

	out := buf.String()
	for _, want := range []string{"level=INFO", "msg=", "user=42", "sent=3", "time="} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// A level below the configured one must not be emitted; one at or above must.
func TestNewFiltersByLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn)

	l.Info("invisible")
	l.Warn("visible")

	out := buf.String()
	if strings.Contains(out, "invisible") {
		t.Errorf("INFO was emitted with level WARN: %q", out)
	}
	if !strings.Contains(out, "visible") {
		t.Errorf("WARN was not emitted with level WARN: %q", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("output does not state the level: %q", out)
	}
}

// Tests need a sink that never complains and never writes; Discard must always
// return a usable logger.
func TestDiscardIsUsable(t *testing.T) {
	l := Discard()
	if l == nil {
		t.Fatal("Discard returned nil")
	}
	l.Info("discarded", "user", 1)
	l.Error("discarded", "err", "x")
}
