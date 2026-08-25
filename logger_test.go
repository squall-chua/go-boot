package goboot

import (
	"context"
	"log/slog"
	"testing"
)

// TestLoggerFromNeverReturnsNil pins the promise in docs/spec.md 4.1: a
// Service Layer function can call LoggerFrom on any context, including one
// that never went near a Transport, and log without a nil check.
func TestLoggerFromNeverReturnsNil(t *testing.T) {
	t.Parallel()
	if LoggerFrom(context.Background()) == nil {
		t.Fatal("LoggerFrom on a bare context returned nil")
	}
}

// TestWithLoggerRoundTrips pins that what RequestID puts in is what the
// handler gets out, and that the two Transports can share one key.
func TestWithLoggerRoundTrips(t *testing.T) {
	t.Parallel()
	want := slog.Default().With("requestId", "abc")
	ctx := WithLogger(context.Background(), want)
	if got := LoggerFrom(ctx); got != want {
		t.Fatalf("LoggerFrom = %p, want %p", got, want)
	}
}

// TestWithLoggerIgnoresNil keeps a nil logger from turning into a nil return
// one call later, which would break the promise above.
func TestWithLoggerIgnoresNil(t *testing.T) {
	t.Parallel()
	if LoggerFrom(WithLogger(context.Background(), nil)) == nil {
		t.Fatal("a nil logger stored in the context came back as nil")
	}
}
