package observability

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// RequestID is the middleware that mints or accepts a request id, echoes it in
// the X-Request-Id response header (API.md §2.3), and threads it through the
// context so every log line of the request carries it (ENGINEERING.md §13.2).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// AccessLog logs one line per request with its outcome (ENGINEERING.md §13.2:
// the middleware logs the request once with the result).
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Info(
				"http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}

// statusRecorder captures the response status code for the access log. It must
// forward the optional-response-writer interfaces the underlying writer
// implements — notably http.Hijacker, without which a WebSocket upgrade behind
// the middleware chain fails (coder/websocket Accept requires it, DEVOPS.md §8
// e2e). http.Flusher and io.ReaderFrom are forwarded for the same reason.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status before delegating.
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack forwards to the underlying writer so hijacking handlers (the
// realtime WebSocket gateway) keep working when wrapped by the access log.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("observability: underlying ResponseWriter is not an http.Hijacker")
	}
	return hj.Hijack()
}

// Flush forwards buffered output (streaming handlers).
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom forwards efficient copy for io.Copy callers.
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req_000000000000"
	}
	return "req_" + hex.EncodeToString(b)
}
