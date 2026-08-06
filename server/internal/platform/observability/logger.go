// Package observability configures the platform's logging (ENGINEERING.md §13)
// and the HTTP middleware that keep every log line joinable to its request.
//
// Logging uses the standard library log/slog: JSON in non-local environments,
// human-readable text in dev. A ContextHandler automatically injects the
// request_id from the request context into every log record, so call sites
// never need to add it manually.
package observability

import (
	"context"
	"log/slog"
	"os"
)

// ctxKey is a private key type for context values in this package.
type ctxKey struct{ name string }

var requestIDKey = ctxKey{"request_id"}

// NewLogger returns the process logger for the given environment.
// In dev it is human-readable text at debug level; elsewhere it is structured
// JSON at info level. All records carry the ambient request_id when present.
func NewLogger(env string) *slog.Logger {
	var handler slog.Handler

	switch env {
	case "dev":
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	default:
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}

	return slog.New(&ContextHandler{handler: handler})
}

// ContextHandler injects the request_id (and future trace_id) from the context
// into every log record handled while a request is in flight.
type ContextHandler struct {
	handler slog.Handler
}

// Enabled reports whether the wrapped handler handles the level.
func (h *ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle adds ambient context attributes, then delegates to the wrapped handler.
func (h *ContextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		rec.AddAttrs(slog.String("request_id", id))
	}
	return h.handler.Handle(ctx, rec)
}

// WithAttrs returns a new ContextHandler with the extra attributes.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{handler: h.handler.WithAttrs(attrs)}
}

// WithGroup returns a new ContextHandler with the given group.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{handler: h.handler.WithGroup(name)}
}

// WithRequestID returns a context carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
