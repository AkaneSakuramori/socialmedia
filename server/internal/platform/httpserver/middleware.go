package httpserver

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
)

// Recover converts panics into RFC 9457 500 responses and logs the stack.
// Domain code never recovers on its own; only the process boundary recovers
// (ENGINEERING.md §14.3).
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error(
						"panic recovered",
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					apierr.Write(w, r, apierr.Internal("an internal error occurred"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
