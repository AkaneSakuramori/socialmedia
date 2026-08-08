package ws

import (
	"sync"
	"time"
)

// limiter enforces the per-connection WS frame budgets (API.md §16.8,
// SECURITY_SPEC.md WS-3). Ephemeral frames are throttled per target exactly as
// Appendix B specifies; a breach is acked RATE_LIMITED and, when sustained,
// closes the socket with 4501.
//
// Budgets (API.md Appendix B):
//
//	ws_typing   2 s     1 per conversation
//	ws_presence 1 s     1 per user
//	ws_read     500 ms  1 per conversation
//	standard    1 min   300 per user+device (all frames)
//
// Durable events (message.*) are not coalesced but still count toward the
// standard frame budget (WS-3).
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	now     func() time.Time
}

// newLimiter builds an empty limiter.
func newLimiter(now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{buckets: make(map[string]*tokenBucket), now: now}
}

// allow reports whether a frame of the given class passes. class selects the
// tier (typing/presence/read/standard); key scopes the bucket (conversation or
// user id).
func (l *limiter) allow(class, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[class+":"+key]
	cfg, ok := budgetFor(class)
	if !ok {
		return true
	}
	if b == nil {
		b = newTokenBucket(cfg.capacity, cfg.interval)
		l.buckets[class+":"+key] = b
	}
	return b.take(l.now())
}

// budget is one Appendix B tier.
type budget struct {
	capacity float64
	interval time.Duration
}

// budgetFor returns the WS-relevant tiers.
func budgetFor(class string) (budget, bool) {
	switch class {
	case "typing":
		return budget{capacity: 1, interval: 2 * time.Second}, true // ws_typing
	case "presence":
		return budget{capacity: 1, interval: 1 * time.Second}, true // ws_presence
	case "read":
		return budget{capacity: 1, interval: 500 * time.Millisecond}, true // ws_read
	case "standard":
		return budget{capacity: 300, interval: time.Minute}, true // standard
	default:
		return budget{}, false
	}
}

// tokenBucket is a fixed-capacity refilling bucket: it starts full and refills
// one token every interval/capacity. take consumes one token when available.
type tokenBucket struct {
	capacity float64
	interval time.Duration
	tokens   float64
	last     time.Time
}

func newTokenBucket(capacity float64, interval time.Duration) *tokenBucket {
	return &tokenBucket{capacity: capacity, interval: interval, tokens: capacity}
}

func (b *tokenBucket) take(now time.Time) bool {
	elapsed := now.Sub(b.last)
	if b.last.IsZero() {
		elapsed = b.interval
	}
	b.tokens += elapsed.Seconds() / b.interval.Seconds() * b.capacity
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// rateLimitViolations bounds sustained abuse before the socket is closed 4501
// (API.md §16.8: "sustained abuse gets the socket closed with code 4501").
const (
	rateLimitMaxViolations   = 10
	rateLimitViolationWindow = 30 * time.Second
)

// connRate bundles one connection's frame limiter and its sustained-abuse
// counter.
type connRate struct {
	mu         sync.Mutex
	limiter    *limiter
	violations int
	firstAt    time.Time
	now        func() time.Time
}

func newConnRate() *connRate {
	return &connRate{limiter: newLimiter(nil)}
}

// allow checks a frame class/key against the budget and, on breach, records a
// violation. It returns whether the frame may proceed.
func (r *connRate) allow(class, key string) bool {
	if r.limiter.allow(class, key) {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now
	if now == nil {
		now = time.Now
	}
	t := now()
	if r.violations == 0 {
		r.firstAt = t
	}
	r.violations++
	return false
}

// abusing reports whether the connection has breached the sustained-abuse
// threshold: too many violations within the window.
func (r *connRate) abusing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now
	if now == nil {
		now = time.Now
	}
	if time.Since(r.firstAt) > rateLimitViolationWindow {
		r.violations = 0
		return false
	}
	return r.violations >= rateLimitMaxViolations
}
