package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// withTestPrincipal injects the authenticated principal before the idempotency
// middleware runs, mirroring the real RequireAuth-outside-Idempotency order.
func withTestPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(WithPrincipal(r.Context(), Principal{
			User:     &userdomain.User{ID: 7},
			DeviceID: "dev-1",
		}))
		next.ServeHTTP(w, r)
	})
}

// idemHandler counts executions and writes a fixed 200 body.
func idemHandler(counter *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		w.Header().Set("X-Run", "yes")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "created")
	})
}

func newIdemClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: s.Addr()}), s
}

func idemScopeKey(t *testing.T, userID int64, key string) string {
	t.Helper()
	scope := fmt.Sprintf("%d:%s", userID, key)
	sum := sha256.Sum256([]byte(scope))
	return "idem:" + hex.EncodeToString(sum[:])
}

func TestIdempotencyRequiresKey(t *testing.T) {
	client, _ := newIdemClient(t)
	h := withTestPrincipal(Idempotency(client)(idemHandler(new(atomic.Int64))))

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if code := errorCode(t, rec); code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", code)
	}
}

func TestIdempotencyRejectsOverlongKey(t *testing.T) {
	client, _ := newIdemClient(t)
	h := withTestPrincipal(Idempotency(client)(idemHandler(new(atomic.Int64))))

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	req.Header.Set("Idempotency-Key", strings.Repeat("k", 256))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestIdempotencyRequiresPrincipal(t *testing.T) {
	client, _ := newIdemClient(t)
	h := Idempotency(client)(idemHandler(new(atomic.Int64))) // no principal wrapper

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestIdempotencyReplaysOnRetry(t *testing.T) {
	client, _ := newIdemClient(t)
	var counter atomic.Int64
	h := withTestPrincipal(Idempotency(client)(idemHandler(&counter)))

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
		r.Header.Set("Idempotency-Key", "k1")
		return r
	}

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req())
	if rec1.Code != http.StatusOK || rec1.Body.String() != "created" {
		t.Fatalf("first call: status=%d body=%q", rec1.Code, rec1.Body.String())
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req())
	if rec2.Code != http.StatusOK || rec2.Body.String() != "created" {
		t.Fatalf("retry: status=%d body=%q", rec2.Code, rec2.Body.String())
	}
	if got := rec2.Header().Get("X-Run"); got != "yes" {
		t.Errorf("replayed headers lost X-Run (got %q)", got)
	}
	if counter.Load() != 1 {
		t.Errorf("handler executed %d times, want 1 (replay)", counter.Load())
	}
}

func TestIdempotencyScopesByUser(t *testing.T) {
	client, s := newIdemClient(t)
	var counter atomic.Int64
	inner := idemHandler(&counter)

	// Two different users using the same key must both execute. The principal
	// is injected outside Idempotency (mirroring RequireAuth ordering).
	chain7 := withPrincipalFor(7, Idempotency(client)(inner))
	chain8 := withPrincipalFor(8, Idempotency(client)(inner))

	for _, h := range []http.Handler{chain7, chain8} {
		req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
		req.Header.Set("Idempotency-Key", "shared")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if counter.Load() != 2 {
		t.Errorf("handler executed %d times, want 2 (per-user scoping)", counter.Load())
	}
	if s.Exists(idemScopeKey(t, 8, "shared")) != true {
		t.Error("user 8's response was not cached")
	}
}

func TestIdempotencyNeverCaches5xx(t *testing.T) {
	client, _ := newIdemClient(t)
	var counter atomic.Int64
	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	h := withTestPrincipal(Idempotency(client)(boom))

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
		r.Header.Set("Idempotency-Key", "k5xx")
		return r
	}

	h.ServeHTTP(httptest.NewRecorder(), req())
	h.ServeHTTP(httptest.NewRecorder(), req())
	if counter.Load() != 2 {
		t.Errorf("handler executed %d times, want 2 (5xx never cached)", counter.Load())
	}
}

func TestIdempotencyRejectsConcurrentDuplicate(t *testing.T) {
	client, _ := newIdemClient(t)
	h := withTestPrincipal(Idempotency(client)(idemHandler(new(atomic.Int64))))

	// Pre-acquire the lock, simulating a concurrent in-flight request.
	lockKey := idemScopeKey(t, 7, "inflight") + ":lock"
	if ok, err := client.SetNX(context.Background(), lockKey, "1", 0).Result(); err != nil || !ok {
		t.Fatalf("pre-acquire lock: %v, %v", ok, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	req.Header.Set("Idempotency-Key", "inflight")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code := errorCode(t, rec); code != "CONFLICT" {
		t.Errorf("code = %q, want CONFLICT", code)
	}
}

func TestIdempotencyRejectsEmptyKey(t *testing.T) {
	client, _ := newIdemClient(t)
	h := withTestPrincipal(Idempotency(client)(idemHandler(new(atomic.Int64))))

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	req.Header.Set("Idempotency-Key", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestReplayCorruptCacheFailsClosed(t *testing.T) {
	client, s := newIdemClient(t)
	h := withTestPrincipal(Idempotency(client)(idemHandler(new(atomic.Int64))))

	key := idemScopeKey(t, 7, "corrupt")
	if err := s.Set(key, "not-json"); err != nil {
		t.Fatalf("seed corrupt cache: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", nil)
	req.Header.Set("Idempotency-Key", "corrupt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// withPrincipalFor binds the given user id as the authenticated principal.
func withPrincipalFor(userID int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(WithPrincipal(r.Context(), Principal{
			User:     &userdomain.User{ID: userID},
			DeviceID: "dev-1",
		}))
		next.ServeHTTP(w, r)
	})
}
