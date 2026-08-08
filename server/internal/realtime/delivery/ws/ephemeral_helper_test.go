package ws

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
)

func newTypingTestStore(t *testing.T) typing.Store {
	t.Helper()
	m := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return typing.NewRedisStore(rc, typing.DefaultConfig())
}

func newPresenceTestStore(t *testing.T) presence.Store {
	t.Helper()
	m := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	cfg := presence.DefaultConfig()
	cfg.Instance = "node-0"
	return presence.NewRedisStore(rc, cfg)
}

// recordingEphemeralNotifier implements both backplane notifiers and records
// every fan-out so handler tests can assert on presence/typing broadcasts.
type recordingEphemeralNotifier struct {
	mu       sync.Mutex
	presence []presence.ChangeEvent
	typing   []typing.ChangeEvent
}

func (n *recordingEphemeralNotifier) NotifyPresence(_ context.Context, ev presence.ChangeEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.presence = append(n.presence, ev)
}

func (n *recordingEphemeralNotifier) NotifyTyping(_ context.Context, ev typing.ChangeEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.typing = append(n.typing, ev)
}
