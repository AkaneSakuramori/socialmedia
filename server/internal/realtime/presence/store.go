// Package presence implements the ephemeral online/offline state and
// last-seen tracking for the realtime module (ARCHITECTURE.md §15, API.md
// §17.11/§18.12). Presence is Redis-backed only: nothing here touches
// PostgreSQL, and every key is TTL'd so stale state self-heals after a
// crashed gateway (heartbeat-based presence expiration, ENGINEERING.md
// §18.3).
//
// A user is online if ANY gateway instance has at least one live connection
// for them. Each instance registers its connections under a per-instance
// key, so a disconnect on one instance cannot flip a user offline while
// another instance still holds a live socket (multi-instance aggregation and
// stale-disconnect protection).
package presence

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Key layout (ENGINEERING.md §7.2 key convention; versioned prefix so a key
// change is a deliberate cache-invalidation incident):
//
//	presence:v1:online:{user}           hash  instance → "1"        (TTL = TTL)
//	presence:v1:conns:{user}:{instance} set   connID                (TTL = TTL)
//	presence:v1:convs:{user}            set   conversationID        (TTL = ConvsTTL)
//	presence:v1:meta:{user}             hash  status, custom_status (TTL = TTL)
//	presence:v1:last_seen:{user}        string RFC3339              (TTL = LastSeenTTL)

func onlineKey(user int64) string { return "presence:v1:online:" + strconv.FormatInt(user, 10) }
func connsKey(user int64, instance string) string {
	return "presence:v1:conns:" + strconv.FormatInt(user, 10) + ":" + instance
}
func convsKey(user int64) string { return "presence:v1:convs:" + strconv.FormatInt(user, 10) }
func metaKey(user int64) string  { return "presence:v1:meta:" + strconv.FormatInt(user, 10) }
func lastSeenKey(user int64) string {
	return "presence:v1:last_seen:" + strconv.FormatInt(user, 10)
}

// Config tunes the presence store's TTLs and instance identity.
type Config struct {
	// Instance identifies this gateway process in the presence aggregation
	// keys (multi-instance propagation, ARCHITECTURE.md §15). Distinct
	// processes must use distinct values (e.g. hostname or node id).
	Instance string
	// TTL is how long a presence heartbeat stays valid without a refresh
	// (API.md §16.7: ~60s idle budget). A crashed gateway's users expire here.
	TTL time.Duration
	// LastSeenTTL retains the last-seen timestamp after a user goes offline
	// (ARCHITECTURE.md §15.1: "last seen hours/days ago").
	LastSeenTTL time.Duration
	// ConvsTTL retains the user's conversation-interest set used to scope
	// presence fan-out (§15.2). Longer than TTL so a user coming back online
	// is still routed to the right conversations.
	ConvsTTL time.Duration
}

// DefaultConfig returns the production-default presence TTLs.
func DefaultConfig() Config {
	return Config{
		TTL:         60 * time.Second,
		LastSeenTTL: 30 * 24 * time.Hour,
		ConvsTTL:    24 * time.Hour,
	}
}

// Change is the outcome of a connect/disconnect lifecycle op. None means the
// user's global online state did not change; Online/Offline mean it flipped
// and a presence.changed broadcast is warranted.
type Change int

const (
	ChangeNone Change = iota
	ChangeOnline
	ChangeOffline
)

func (c Change) String() string {
	switch c {
	case ChangeOnline:
		return "online"
	case ChangeOffline:
		return "offline"
	default:
		return "none"
	}
}

// Store tracks ephemeral presence state. The Redis implementation is in
// RedisStore; a nil-safe no-op variant is used where presence is optional.
type Store interface {
	// Connect registers a connection of a user on an instance and reports
	// whether the user transitioned offline → online (first instance online).
	Connect(ctx context.Context, userID int64, connID, instance string) (Change, error)
	// Disconnect removes a connection. ChangeOffline is returned only when
	// this removal caused the user to go fully offline (no live connections
	// anywhere); a stale or redundant removal returns ChangeNone.
	Disconnect(ctx context.Context, userID int64, connID, instance string) (Change, error)
	// Touch refreshes the instance's presence liveness (heartbeat sweeper).
	Touch(ctx context.Context, userID int64, instance string) error
	// IsOnline reports whether the user has any live connection on any
	// instance.
	IsOnline(ctx context.Context, userID int64) (bool, error)
	// SetMeta records the user's presence.status/custom_status.
	SetMeta(ctx context.Context, userID int64, status, customStatus string) error
	// GetMeta reads the stored presence meta (zero values when absent).
	GetMeta(ctx context.Context, userID int64) (status, customStatus string, err error)
	// SetLastSeen records when the user went offline.
	SetLastSeen(ctx context.Context, userID int64, at time.Time) error
	// GetLastSeen reads the last-seen timestamp (zero when absent).
	GetLastSeen(ctx context.Context, userID int64) (time.Time, error)
	// ConvsAdd/ConvsRemove maintain the user's conversation-interest set
	// (SADD/SREM, best-effort — used to scope presence fan-out).
	ConvsAdd(ctx context.Context, userID int64, convID int64) error
	ConvsRemove(ctx context.Context, userID int64, convID int64) error
	// Convs lists the user's conversation-interest set.
	Convs(ctx context.Context, userID int64) ([]int64, error)
}

// connectScript atomically registers a connection and returns 1 when the user
// was offline (transition to online). Serialized by Redis, so concurrent
// connects from multiple instances produce exactly one online transition.
var connectScript = redis.NewScript(`
local was = redis.call('EXISTS', KEYS[1])
redis.call('SADD', KEYS[2], ARGV[1])
redis.call('EXPIRE', KEYS[2], ARGV[2])
redis.call('HSET', KEYS[1], ARGV[3], '1')
redis.call('EXPIRE', KEYS[1], ARGV[2])
if was == 0 then
	return 1
end
return 0
`)

// disconnectScript atomically removes a connection. Returns 2 when the user
// went fully offline (and records last_seen), 1 when still online, 0 when the
// removal was stale (the connection was not registered).
var disconnectScript = redis.NewScript(`
local removed = redis.call('SREM', KEYS[2], ARGV[1])
if removed == 0 then
	return 0
end
local n = redis.call('SCARD', KEYS[2])
if n > 0 then
	return 1
end
redis.call('DEL', KEYS[2])
redis.call('HDEL', KEYS[1], ARGV[2])
local left = redis.call('HLEN', KEYS[1])
if left == 0 then
	redis.call('DEL', KEYS[1])
	redis.call('SET', KEYS[3], ARGV[3])
	redis.call('EXPIRE', KEYS[3], ARGV[4])
	return 2
end
return 1
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

func (s *RedisStore) Connect(ctx context.Context, userID int64, connID, instance string) (Change, error) {
	res, err := connectScript.Run(ctx, s.client,
		[]string{onlineKey(userID), connsKey(userID, instance)},
		connID, int64(s.cfg.TTL.Seconds()), instance,
	).Int64()
	if err != nil {
		return ChangeNone, fmt.Errorf("presence: connect %d: %w", userID, err)
	}
	if res == 1 {
		return ChangeOnline, nil
	}
	return ChangeNone, nil
}

func (s *RedisStore) Disconnect(ctx context.Context, userID int64, connID, instance string) (Change, error) {
	res, err := disconnectScript.Run(ctx, s.client,
		[]string{onlineKey(userID), connsKey(userID, instance), lastSeenKey(userID)},
		connID, instance, time.Now().UTC().Format(time.RFC3339), int64(s.cfg.LastSeenTTL.Seconds()),
	).Int64()
	if err != nil {
		return ChangeNone, fmt.Errorf("presence: disconnect %d: %w", userID, err)
	}
	switch res {
	case 2:
		return ChangeOffline, nil
	case 1:
		return ChangeNone, nil
	default:
		return ChangeNone, nil
	}
}

func (s *RedisStore) Touch(ctx context.Context, userID int64, instance string) error {
	if err := s.client.Expire(ctx, onlineKey(userID), s.cfg.TTL).Err(); err != nil {
		return fmt.Errorf("presence: touch online %d: %w", userID, err)
	}
	if err := s.client.Expire(ctx, connsKey(userID, instance), s.cfg.TTL).Err(); err != nil {
		return fmt.Errorf("presence: touch conns %d: %w", userID, err)
	}
	return nil
}

func (s *RedisStore) IsOnline(ctx context.Context, userID int64) (bool, error) {
	n, err := s.client.Exists(ctx, onlineKey(userID)).Result()
	if err != nil {
		return false, fmt.Errorf("presence: online %d: %w", userID, err)
	}
	return n == 1, nil
}

func (s *RedisStore) SetMeta(ctx context.Context, userID int64, status, customStatus string) error {
	if err := s.client.HSet(ctx, metaKey(userID), map[string]any{
		"status": status, "custom_status": customStatus,
	}).Err(); err != nil {
		return fmt.Errorf("presence: set meta %d: %w", userID, err)
	}
	if err := s.client.Expire(ctx, metaKey(userID), s.cfg.TTL).Err(); err != nil {
		return fmt.Errorf("presence: meta ttl %d: %w", userID, err)
	}
	return nil
}

func (s *RedisStore) GetMeta(ctx context.Context, userID int64) (string, string, error) {
	res, err := s.client.HGetAll(ctx, metaKey(userID)).Result()
	if err != nil {
		return "", "", fmt.Errorf("presence: get meta %d: %w", userID, err)
	}
	return res["status"], res["custom_status"], nil
}

func (s *RedisStore) SetLastSeen(ctx context.Context, userID int64, at time.Time) error {
	if err := s.client.Set(ctx, lastSeenKey(userID), at.UTC().Format(time.RFC3339), s.cfg.LastSeenTTL).Err(); err != nil {
		return fmt.Errorf("presence: set last_seen %d: %w", userID, err)
	}
	return nil
}

func (s *RedisStore) GetLastSeen(ctx context.Context, userID int64) (time.Time, error) {
	v, err := s.client.Get(ctx, lastSeenKey(userID)).Result()
	if err != nil {
		if err == redis.Nil {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("presence: get last_seen %d: %w", userID, err)
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("presence: last_seen %d: %w", userID, err)
	}
	return t, nil
}

func (s *RedisStore) ConvsAdd(ctx context.Context, userID int64, convID int64) error {
	if err := s.client.SAdd(ctx, convsKey(userID), convID).Err(); err != nil {
		return fmt.Errorf("presence: convs add %d: %w", userID, err)
	}
	if err := s.client.Expire(ctx, convsKey(userID), s.cfg.ConvsTTL).Err(); err != nil {
		return fmt.Errorf("presence: convs ttl %d: %w", userID, err)
	}
	return nil
}

func (s *RedisStore) ConvsRemove(ctx context.Context, userID int64, convID int64) error {
	if err := s.client.SRem(ctx, convsKey(userID), convID).Err(); err != nil {
		return fmt.Errorf("presence: convs remove %d: %w", userID, err)
	}
	return nil
}

func (s *RedisStore) Convs(ctx context.Context, userID int64) ([]int64, error) {
	res, err := s.client.SMembers(ctx, convsKey(userID)).Result()
	if err != nil {
		return nil, fmt.Errorf("presence: convs %d: %w", userID, err)
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
