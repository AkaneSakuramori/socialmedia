// Package redis provides the go-redis client for the platform (ENGINEERING.md
// §2, §22). Used for caching, presence, sessions, idempotency and pub/sub by
// future domains; the foundation wires it and checks it for readiness.
package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewClient creates a Redis client. It does not connect eagerly; callers
// should Ping at startup and rely on the readiness check for runtime status.
func NewClient(addr, password string, db int) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
}

// Ping checks Redis connectivity with a timeout.
func Ping(ctx context.Context, client *redis.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return client.Ping(ctx).Err()
}
