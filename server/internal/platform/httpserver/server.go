// Package httpserver bootstraps the HTTP server: routing, middleware order,
// timeouts, and graceful shutdown. It is the delivery/transport layer of the
// api-server process; domain handlers are mounted by the composition root as
// features land (ENGINEERING.md §8, §9).
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/AkaneSakuramori/socialmedia/server/config"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
)

// New assembles the HTTP server with its middleware chain, the two health
// probes, and the domain routes provided by the composition root. Middleware
// order (outermost first): access log, panic recovery, request-id — so every
// log line and every response header is correlated. extra may be nil; it is
// mounted under its own paths (domain handlers register their routes).
func New(cfg config.Config, log *slog.Logger, liveness, readiness http.Handler, extra http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", liveness)
	mux.Handle("GET /readyz", readiness)
	if extra != nil {
		mux.Handle("/", extra)
	}
	mux.HandleFunc("/", notFound)

	var h http.Handler = mux
	h = observability.AccessLog(log)(h)
	h = Recover(log)(h)
	h = observability.RequestID(h)

	return &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
}

// Shutdown gracefully drains in-flight requests within the caller's context
// (ENGINEERING.md §10.2 lifecycle, DEVOPS.md §5).
func Shutdown(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

// notFound returns the RFC 9457 error envelope for unknown routes.
func notFound(w http.ResponseWriter, r *http.Request) {
	apierr.Write(w, r, apierr.NotFound("resource not found"))
}

// IsServerClosed reports whether err is the expected server-closed error.
func IsServerClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed)
}
