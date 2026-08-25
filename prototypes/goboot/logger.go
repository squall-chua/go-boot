package goboot

import (
	"context"
	"log/slog"
)

type loggerKey struct{}

// WithLogger is what the RequestID middleware calls. It lives in base so both
// Transports share one key.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, log)
}

// LoggerFrom returns the request-scoped logger, already carrying the request
// ID. It never returns nil.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
