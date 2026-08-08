// Package typing implements ephemeral conversation-scoped typing indicators
// for the realtime module (ARCHITECTURE.md §16, API.md §17.10/§18.11).
// Typing state lives in Redis with a short TTL (auto-expiry), is throttled per
// (user, conversation), and is cleaned up on disconnect. It is never persisted
// and never replayed on reconnect (ephemeral by design).
package typing

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Key layout (ENGINEERING.md §7.2; versioned prefix):
//
//	typing:v1:{conv}      hash userID → last broadcast unix-ms (TTL = Expiry)
//	typing:v1:user:{user} set  convID (TTL = Expiry) — for disconnect cleanup

func convKey(convID int64) string { return "typing:v1:" + strconv.FormatInt(convID, 10) }
func userKey(userID int64) string {
	return "typing:v1:user:" + strconv.FormatInt(userID, 10)
}

// Config tunes the typing store.
type Config struct {
	// Expiry is how long a typing state stays live without a refresh
	// (ARCHITECTURE.md §16: TTL ~10s; API.md §17.10: indicators auto-expire
	// after 5s). A missed typing.stop cannot leave typing stuck forever.
	Expiry time.Duration
	// Throttle suppresses redundant typing broadcasts per (user, conversation)
	// (API.md §16.8/Appendix B: ws_typing max 1 per 2s).
	Throttle time.Duration
}

// DefaultConfig returns the production-default typing TTLs.
func DefaultConfig() Config {
	return Config{Expiry: 10 * time.Second, Throttle: 2 * time.Second}
}

// Store tracks typing state. RedisStore is the production implementation.
type Store interface {
	// Broadcast records a typing signal for (user, conv) and reports whether a
	// typing.indicator broadcast is allowed (not throttled). The state TTL is
	// always refreshed so active typing never expires.
	Broadcast(ctx context.Context, userID, convID int64) (bool, error)
	// Stop removes the user's typing state from a conversation and reports
	// whether a "stopped" broadcast should be sent (always true unless absent).
	Stop(ctx context.Context, userID, convID int64) (bool, error)
	// CleanupUser removes the user's typing state from every conversation they
	// were typing in and returns those conversation ids (disconnect cleanup).
	CleanupUser(ctx context.Context, userID int64) ([]int64, error)
	// IsTyping reports whether the user currently has typing state in conv.
	IsTyping(ctx context.Context, userID, convID int64) (bool, error)
}

// broadcastScript records the typing signal atomically. Throttle is computed
// against the Redis server clock (TIME) so concurrent gateway instances share
// one throttle window regardless of local clock skew. Returns 1 when the
// broadcast should be sent (not within the throttle window), 0 when throttled.
var broadcastScript = redis.NewScript(`
local conv = KEYS[1]
local usr = KEYS[2]
local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)
local throttle_ms = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local last = redis.call('HGET', conv, ARGV[3])
if last and (now_ms - tonumber(last)) < throttle_ms then
	redis.call('EXPIRE', conv, ttl)
	redis.call('EXPIRE', usr, ttl)
	return 0
end
redis.call('HSET', conv, ARGV[3], now_ms)
redis.call('EXPIRE', conv, ttl)
redis.call('SADD', usr, ARGV[4])
redis.call('EXPIRE', usr, ttl)
return 1
`)

// stopScript removes the user's typing state and reports whether it existed
// (so the caller can broadcast "stopped").
var stopScript = redis.NewScript(`
local removed = redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('SREM', KEYS[2], ARGV[2])
local left = redis.call('HLEN', KEYS[1])
if left == 0 then
	redis.call('DEL', KEYS[1])
end
return removed
`)

// cleanupScript removes a user from every conversation they were typing in and
// returns the conversation ids (as a reply array) so the caller can broadcast
// "stopped" to each.
var cleanupScript = redis.NewScript(`
local usr = KEYS[1]
local prefix = ARGV[2]
local convs = redis.call('SMEMBERS', usr)
for _, id in ipairs(convs) do
	local conv = prefix .. id
	redis.call('HDEL', conv, ARGV[1])
	local left = redis.call('HLEN', conv)
	if left == 0 then
		redis.call('DEL', conv)
	end
end
redis.call('DEL', usr)
return convs
`)

// RedisStore is the production Store over go-redis.
type RedisStore struct {
	client *redis.Client
	cfg    Config
}

// NewRedisStore builds the store over the shared Redis client.
func NewRedisStore(client *redis.Client, cfg Config) *RedisStore {
	return &RedisStore{client: client, cfg: cfg}
}

func (s *RedisStore) Broadcast(ctx context.Context, userID, convID int64) (bool, error) {
	ok, err := broadcastScript.Run(ctx, s.client,
		[]string{convKey(convID), userKey(userID)},
		int64(s.cfg.Throttle.Milliseconds()), int64(s.cfg.Expiry.Seconds()),
		strconv.FormatInt(userID, 10), strconv.FormatInt(convID, 10),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("typing: broadcast %d/%d: %w", userID, convID, err)
	}
	return ok == 1, nil
}

func (s *RedisStore) Stop(ctx context.Context, userID, convID int64) (bool, error) {
	n, err := stopScript.Run(ctx, s.client,
		[]string{convKey(convID), userKey(userID)},
		strconv.FormatInt(userID, 10), strconv.FormatInt(convID, 10),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("typing: stop %d/%d: %w", userID, convID, err)
	}
	return n == 1, nil
}

func (s *RedisStore) CleanupUser(ctx context.Context, userID int64) ([]int64, error) {
	res, err := cleanupScript.Run(ctx, s.client,
		[]string{userKey(userID)},
		strconv.FormatInt(userID, 10), "typing:v1:",
	).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("typing: cleanup %d: %w", userID, err)
	}
	out := make([]int64, 0, len(res))
	for _, v := range res {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *RedisStore) IsTyping(ctx context.Context, userID, convID int64) (bool, error) {
	n, err := s.client.HExists(ctx, convKey(convID), strconv.FormatInt(userID, 10)).Result()
	if err != nil {
		return false, fmt.Errorf("typing: is_typing %d/%d: %w", userID, convID, err)
	}
	return n, nil
}
