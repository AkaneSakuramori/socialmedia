package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// Idempotency-Key middleware (API.md §2.7, ENGINEERING.md §29). Unsafe writes
// must present a client-generated key (≤255 chars); the middleware stores the
// hashed key → response in Redis for 24 h and replays it on a retry, so
// at-least-once client retries can never create duplicates. Validation failures
// (4xx before execution) are never cached. Scope is (user, key).

const (
	idemTTL     = 24 * time.Hour
	idemLockTTL = 10 * time.Second
	// idemCacheStatuses gate what gets cached: cache all non-5xx responses so a
	// retry of a rejected write is also replayed consistently, but never cache
	// server faults (5xx are retried with backoff instead of replayed).
)

// Idempotency returns the middleware that wraps unsafe-write handlers. keyScope
// derives the per-request scoping (typically the authenticated user id); when
// it is empty the request is rejected, since idempotency must be per-user.
func Idempotency(client *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				WriteError(w, r, validationErr("Idempotency-Key", "required"))
				return
			}
			if len(key) > 255 {
				WriteError(w, r, validationErr("Idempotency-Key", "too_long"))
				return
			}

			p, ok := PrincipalFrom(r.Context())
			if !ok {
				WriteError(w, r, apierrUnauthorized("idempotency requires an authenticated caller"))
				return
			}

			scope := fmt.Sprintf("%d:%s", p.UserID(), key)
			sum := sha256.Sum256([]byte(scope))
			ck := "idem:" + hex.EncodeToString(sum[:])

			// Replay a stored response if present.
			if cached, err := client.Get(r.Context(), ck).Bytes(); err == nil {
				replay(w, r, cached)
				return
			} else if err != redis.Nil {
				// Redis unavailable: fail closed on writes (a duplicate is
				// worse than a temporary error).
				WriteError(w, r, apierrWrap(err, "idempotency store unavailable"))
				return
			}

			// Serialize concurrent duplicates: only one in-flight request per
			// key may execute; the rest get a 409.
			lockKey := ck + ":lock"
			acquired, err := client.SetNX(r.Context(), lockKey, "1", idemLockTTL).Result()
			if err != nil {
				WriteError(w, r, apierrWrap(err, "idempotency store unavailable"))
				return
			}
			if !acquired {
				WriteError(w, r, apierrConflict("a concurrent request with this Idempotency-Key is in flight"))
				return
			}
			defer client.Del(r.Context(), lockKey)

			rec := &responseRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			// Cache only after the handler completed (i.e. execution started
			// and finished); 5xx responses are never cached.
			if rec.status >= 200 && rec.status < 500 {
				payload, err := json.Marshal(cachedResponse{Status: rec.status, Header: rec.Header(), Body: rec.body.Bytes()})
				if err == nil {
					_ = client.Set(r.Context(), ck, payload, idemTTL).Err()
				}
			}
		})
	}
}

// cachedResponse is the stored replay payload (status + headers + body).
type cachedResponse struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}

// replay writes a previously stored response instead of re-executing.
func replay(w http.ResponseWriter, r *http.Request, payload []byte) {
	var cached cachedResponse
	if err := json.Unmarshal(payload, &cached); err != nil {
		WriteError(w, r, apierrWrap(err, "idempotency cache corrupt"))
		return
	}
	for k, vs := range cached.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(cached.Status)
	_, _ = w.Write(cached.Body)
}

// responseRecorder captures the status and body so the middleware can cache the
// exact response and replay it later.
type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
