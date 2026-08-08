//go:build integration

package presence

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// integRedis connects to the local stack Redis, skipping when unreachable.
func integRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rc.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s, skipping: %v", addr, err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// cleanPresenceUser removes every key the store writes for one user, so an
// integration run is repeatable against a shared dev Redis.
func cleanPresenceUser(t *testing.T, rc *redis.Client, user int64) {
	t.Helper()
	ctx := context.Background()
	_ = rc.Del(ctx,
		"presence:v1:online:"+itoa(user),
		"presence:v1:convs:"+itoa(user),
		"presence:v1:meta:"+itoa(user),
		"presence:v1:last_seen:"+itoa(user),
	)
	t.Cleanup(func() {
		ctx := context.Background()
		_ = rc.Del(ctx,
			"presence:v1:online:"+itoa(user),
			"presence:v1:convs:"+itoa(user),
			"presence:v1:meta:"+itoa(user),
			"presence:v1:last_seen:"+itoa(user),
		)
	})
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestIntegrationPresenceMultiInstanceAggregation(t *testing.T) {
	rc := integRedis(t)
	user := time.Now().UnixNano()
	cleanPresenceUser(t, rc, user)
	ctx := context.Background()

	c0 := DefaultConfig()
	c0.Instance = "node-0"
	c1 := DefaultConfig()
	c1.Instance = "node-1"
	s0 := NewRedisStore(rc, c0)
	s1 := NewRedisStore(rc, c1)

	// Two devices on two gateway instances, same logical user.
	if ch, _ := s0.Connect(ctx, user, "c-a", "node-0"); ch != ChangeOnline {
		t.Fatalf("first connect = %v, want ChangeOnline", ch)
	}
	if ch, _ := s1.Connect(ctx, user, "c-b", "node-1"); ch != ChangeNone {
		t.Errorf("second instance connect = %v, want ChangeNone (already online)", ch)
	}
	if on, _ := s0.IsOnline(ctx, user); !on {
		t.Fatal("user must be online across instances")
	}

	// node-0's connection drops; node-1's keeps the user online.
	if ch, _ := s0.Disconnect(ctx, user, "c-a", "node-0"); ch != ChangeNone {
		t.Errorf("single disconnect = %v, want ChangeNone", ch)
	}
	if on, _ := s1.IsOnline(ctx, user); !on {
		t.Fatal("user must stay online while a remote instance holds a connection")
	}

	// Last connection anywhere drops → fully offline, exactly one transition.
	if ch, _ := s1.Disconnect(ctx, user, "c-b", "node-1"); ch != ChangeOffline {
		t.Errorf("last disconnect = %v, want ChangeOffline", ch)
	}
	if on, _ := s0.IsOnline(ctx, user); on {
		t.Fatal("user must be offline after every instance disconnects")
	}

	// A stale SREM (already gone) must not flip the user offline again.
	if ch, _ := s0.Disconnect(ctx, user, "c-a", "node-0"); ch != ChangeNone {
		t.Errorf("stale disconnect = %v, want ChangeNone", ch)
	}
}

func TestIntegrationPresenceServiceMetaAndLastSeen(t *testing.T) {
	rc := integRedis(t)
	user := time.Now().UnixNano()
	cleanPresenceUser(t, rc, user)
	ctx := context.Background()

	cfg := DefaultConfig()
	cfg.Instance = "node-0"
	store := NewRedisStore(rc, cfg)
	svc := NewService(store, cfg, &recordingNotifier{}, testLogger())

	if !svc.Connect(ctx, user, "c-1") {
		t.Fatal("first connect must flip the user online")
	}
	svc.SetConversation(ctx, user, 2001)
	svc.Update(ctx, user, "busy", "headphones on")
	st, custom, err := store.GetMeta(ctx, user)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if st != "busy" || custom != "headphones on" {
		t.Errorf("meta = %q/%q, want busy/headphones on", st, custom)
	}
	if !svc.Disconnect(ctx, user, "c-1") {
		t.Fatal("last disconnect must flip the user offline")
	}
	last, err := svc.LastSeen(ctx, user)
	if err != nil {
		t.Fatalf("last seen: %v", err)
	}
	if last.IsZero() {
		t.Fatal("last_seen must be set on disconnect")
	}
}
