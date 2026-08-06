package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Handler returns the liveness (/healthz) or readiness (/readyz) handler.
// Liveness is process-alive only; readiness runs all registered dependency
// checks and returns 503 while any of them fail (DEVOPS.md §5).
func Handler(log *slog.Logger, reg *Registry, readiness bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"status": "ok"}

		if readiness {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			ok, results := reg.Ready(ctx)
			body["checks"] = results
			if !ok {
				log.Warn("readiness failed", "checks", len(results))
				body["status"] = "not_ready"
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(body)
				return
			}
			body["status"] = "ready"
		} else {
			body["checks"] = reg.Alive()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	}
}
