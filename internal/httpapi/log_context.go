package httpapi

import (
	"context"
	"log/slog"
)

type loggerCtxKey struct{}

// WithLogger attaches the per-request logger to ctx. Middleware does
// this once after building a logger with req_id + principal_* fields;
// handlers fetch via LoggerFromContext.
func WithLogger(ctx context.Context, lg *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, lg)
}

// LoggerFromContext returns the per-request logger if attached, else
// slog.Default(). Never returns nil.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && lg != nil {
		return lg
	}
	return slog.Default()
}
