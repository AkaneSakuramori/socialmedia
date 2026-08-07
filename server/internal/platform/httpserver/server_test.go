package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/config"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/health"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
)

func newTestServer(t *testing.T) *http.Server {
	t.Helper()
	cfg := config.Config{
		HTTPPort:          "0",
		ReadHeaderTimeout: 5 * time.Second,
	}
	reg := health.NewRegistry()
	reg.Register("postgres", health.CheckFunc(func(context.Context) error { return nil }))
	log := observability.NewLogger("test")
	liveness := health.Handler(log, reg, false)
	readiness := health.Handler(log, reg, true)
	return New(cfg, log, liveness, readiness, nil)
}

func do(t *testing.T, srv *http.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoints(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, "/healthz")
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}

	rec = do(t, srv, "/readyz")
	if rec.Code != http.StatusOK {
		t.Errorf("/readyz status = %d, want 200 (all checks healthy)", rec.Code)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header missing")
	}
}

func TestReadinessFailsWhenDependencyDown(t *testing.T) {
	cfg := config.Config{HTTPPort: "0", ReadHeaderTimeout: 5 * time.Second}
	reg := health.NewRegistry()
	reg.Register("redis", health.CheckFunc(func(context.Context) error { return context.DeadlineExceeded }))
	log := observability.NewLogger("test")
	srv := New(cfg, log, health.Handler(log, reg, false), health.Handler(log, reg, true), nil)

	if rec := do(t, srv, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 503 when dependency down", rec.Code)
	}
	if rec := do(t, srv, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200 (liveness independent of deps)", rec.Code)
	}
}

func TestNotFoundReturnsProblemJSON(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, "/nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" || ct[:17] != "application/probl" {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}
}

func TestPanicRecoveredTo500(t *testing.T) {
	log := observability.NewLogger("test")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	var h http.Handler = mux
	h = observability.AccessLog(log)(h)
	h = Recover(log)(h)
	h = observability.RequestID(h)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}

	rec := do(t, srv, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestShutdownReturnsNilWhenIdle(t *testing.T) {
	srv := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Shutdown(ctx, srv); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
