package typing

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testStore(t *testing.T, cfg Config) (Store, *miniredis.Miniredis) {
	t.Helper()
	m := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return NewRedisStore(rc, cfg), m
}

func testConfig() Config { return DefaultConfig() }

func TestBroadcastThrottlePerUserConversation(t *testing.T) {
	store, m := testStore(t, testConfig())
	ctx := context.Background()

	if ok, _ := store.Broadcast(ctx, 1001, 2001); !ok {
		t.Fatal("first broadcast must be allowed")
	}
	if ok, _ := store.Broadcast(ctx, 1001, 2001); ok {
		t.Fatal("second broadcast within throttle window must be suppressed")
	}
	// A different conversation is a separate budget.
	if ok, _ := store.Broadcast(ctx, 1001, 2002); !ok {
		t.Fatal("broadcast to another conversation must be allowed")
	}
	// A different user in the same conversation is a separate budget.
	if ok, _ := store.Broadcast(ctx, 1002, 2001); !ok {
		t.Fatal("broadcast by another user must be allowed")
	}
	// Fast-forward the server clock past the 2s throttle (SetTime drives
	// Redis TIME): the original budget refills.
	m.SetTime(time.Now().Add(3 * time.Second))
	if ok, _ := store.Broadcast(ctx, 1001, 2001); !ok {
		t.Fatal("broadcast after throttle window must be allowed again")
	}
}

func TestTypingExpiry(t *testing.T) {
	store, m := testStore(t, testConfig())
	ctx := context.Background()

	store.Broadcast(ctx, 1001, 2001)
	if is, _ := store.IsTyping(ctx, 1001, 2001); !is {
		t.Fatal("typing state must exist after broadcast")
	}
	// Missed typing.stop: the 10s TTL auto-clears the state.
	m.FastForward(11 * time.Second)
	if is, _ := store.IsTyping(ctx, 1001, 2001); is {
		t.Fatal("typing state must expire automatically")
	}
}

func TestStopClearsAndReports(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	store.Broadcast(ctx, 1001, 2001)
	ok, err := store.Stop(ctx, 1001, 2001)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !ok {
		t.Error("stop must report the state existed (broadcast stopped)")
	}
	if is, _ := store.IsTyping(ctx, 1001, 2001); is {
		t.Fatal("typing state must be cleared by stop")
	}
	// A second stop (already cleared) must not re-broadcast.
	if ok, _ := store.Stop(ctx, 1001, 2001); ok {
		t.Error("redundant stop must not report existence")
	}
}

func TestCleanupUserAcrossConversations(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	store.Broadcast(ctx, 1001, 2001)
	store.Broadcast(ctx, 1001, 2002)
	store.Broadcast(ctx, 1002, 2001) // another user in conv 2001

	convs, err := store.CleanupUser(ctx, 1001)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("cleaned convs = %v, want both 2001 and 2002", convs)
	}
	if is, _ := store.IsTyping(ctx, 1001, 2001); is {
		t.Fatal("user 1001 typing state in conv 2001 must be cleared")
	}
	if is, _ := store.IsTyping(ctx, 1001, 2002); is {
		t.Fatal("user 1001 typing state in conv 2002 must be cleared")
	}
	// Another user's state in the shared conversation must survive.
	if is, _ := store.IsTyping(ctx, 1002, 2001); !is {
		t.Fatal("user 1002's typing state must survive another user's cleanup")
	}
}

// recordingNotifier captures typing transitions for service tests.
type recordingNotifier struct {
	mu  sync.Mutex
	evs []ChangeEvent
}

func (n *recordingNotifier) NotifyTyping(_ context.Context, ev ChangeEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evs = append(n.evs, ev)
}

func (n *recordingNotifier) all() []ChangeEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ChangeEvent(nil), n.evs...)
}

func newTestService(t *testing.T, n Notifier) *Service {
	t.Helper()
	store, _ := testStore(t, testConfig())
	return NewService(store, testConfig(), n, slog.New(slog.DiscardHandler))
}

func TestServiceStartBroadcastsTyping(t *testing.T) {
	n := &recordingNotifier{}
	svc := newTestService(t, n)

	svc.Start(context.Background(), 1001, 2001)
	evs := n.all()
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Status != "typing" || evs[0].ConversationID != 2001 || evs[0].UserID != 1001 {
		t.Errorf("event = %+v, want typing/2001/1001", evs[0])
	}
	// Immediate repeat is throttled by the service: no second broadcast.
	svc.Start(context.Background(), 1001, 2001)
	if len(n.all()) != 1 {
		t.Error("throttled typing.start must not broadcast")
	}
	// typing.stop is never throttled.
	svc.Stop(context.Background(), 1001, 2001)
	if len(n.all()) != 2 || n.all()[1].Status != "stopped" {
		t.Errorf("expected a stopped broadcast, got %+v", n.all())
	}
}

func TestServiceCleanupUserBroadcastsStopped(t *testing.T) {
	n := &recordingNotifier{}
	svc := newTestService(t, n)

	svc.Start(context.Background(), 1001, 2001)
	svc.Start(context.Background(), 1001, 2002)
	svc.CleanupUser(context.Background(), 1001)

	stopped := 0
	for _, ev := range n.all() {
		if ev.Status == "stopped" {
			stopped++
		}
	}
	if stopped != 2 {
		t.Errorf("stopped broadcasts = %d, want 2 (one per conversation)", stopped)
	}
}
