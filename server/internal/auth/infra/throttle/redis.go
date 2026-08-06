// Package throttle implements the failed-login lockout store (AUTH-5).
package throttle

import (
	"context"
	"errors"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/redis/go-redis/v9"
)

// keyPrefix scopes throttle keys so they never collide with other Redis users
// (presence, sessions, idempotency).
const keyPrefix = "auth:lockout:"

// RedisThrottle stores the consecutive-failure counter per identifier in Redis
// (API.md §4.3: login hot path is a Redis read). The counter's TTL is the
// lockout duration, so a clean lockout window naturally expires the state:
// "5 consecutive failures within 5 minutes → locked for 5 minutes" (AUTH-5).
type RedisThrottle struct {
	client *redis.Client
	policy domain.LoginPolicy
}

// New builds a throttle bound to a login policy. policy.LockoutDuration is the
// TTL applied on every recorded failure.
func New(client *redis.Client, policy domain.LoginPolicy) *RedisThrottle {
	return &RedisThrottle{client: client, policy: policy}
}

// Failures returns the consecutive failure count (0 when none or expired).
func (r *RedisThrottle) Failures(ctx context.Context, identifier string) (int, error) {
	n, err := r.client.Get(ctx, key(identifier)).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RecordFailure increments the counter and slides the expiry to the lockout
// duration, so each failure keeps the window alive.
func (r *RedisThrottle) RecordFailure(ctx context.Context, identifier string) error {
	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, key(identifier))
	pipe.Expire(ctx, key(identifier), r.policy.LockoutDuration)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	if incr.Val() <= 0 {
		// Defensive: never store a non-positive counter.
		return r.client.Del(ctx, key(identifier)).Err()
	}
	return nil
}

// Clear resets the counter after a successful login.
func (r *RedisThrottle) Clear(ctx context.Context, identifier string) error {
	return r.client.Del(ctx, key(identifier)).Err()
}

// LockoutRemaining returns the time the identifier stays locked, or 0 when
// not locked. "Locked" is failures >= policy.MaxFailures; the remaining time
// is the counter's TTL, which the application surfaces as Retry-After.
func (r *RedisThrottle) LockoutRemaining(ctx context.Context, identifier string) (time.Duration, error) {
	n, err := r.Failures(ctx, identifier)
	if err != nil {
		return 0, err
	}
	if n < r.policy.MaxFailures {
		return 0, nil
	}
	ttl, err := r.client.TTL(ctx, key(identifier)).Result()
	if err != nil {
		return 0, err
	}
	if ttl < 0 {
		return 0, nil
	}
	return ttl, nil
}

func key(identifier string) string { return keyPrefix + identifier }
