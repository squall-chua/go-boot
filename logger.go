package goboot

import (
	"context"
	"log/slog"
)

// loggerKey is unexported, so nothing outside this package can write the slot
// behind WithLogger's back.
type loggerKey struct{}

// WithLogger stores the request-scoped logger. It is what the web Starter's
// RequestID middleware calls, and what the gRPC interceptor will call. Both
// live here, in base, so the two Transports share one key: a Service Layer
// function that logs behaves the same whichever Transport called it.
//
// A nil logger is not stored, so LoggerFrom keeps its promise below.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, log)
}

// LoggerFrom returns the request-scoped logger, already carrying the request
// ID. It never returns nil: a context that never went through a Transport
// falls back to slog.Default(), so a Service Layer function needs no nil
// check and no logger parameter.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return log
	}
	return slog.Default()
}
