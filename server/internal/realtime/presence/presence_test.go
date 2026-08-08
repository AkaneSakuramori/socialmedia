package presence

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

func testConfig() Config {
	c := DefaultConfig()
	c.Instance = "node-0"
	return c
}

func TestConnectOnlineTransition(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	if ch, err := store.Connect(ctx, 1001, "c-1", "node-0"); err != nil {
		t.Fatalf("connect: %v", err)
	} else if ch != ChangeOnline {
		t.Errorf("first connect change = %v, want ChangeOnline", ch)
	}
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("user must be online after first connect")
	}
	// Second connection from the same instance: no transition.
	if ch, _ := store.Connect(ctx, 1001, "c-2", "node-0"); ch != ChangeNone {
		t.Errorf("second connect change = %v, want ChangeNone", ch)
	}
}

func TestDisconnectLastConnectionGoesOffline(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	store.Connect(ctx, 1001, "c-1", "node-0")
	store.Connect(ctx, 1001, "c-2", "node-0")
	if ch, _ := store.Disconnect(ctx, 1001, "c-1", "node-0"); ch != ChangeNone {
		t.Errorf("mid disconnect change = %v, want ChangeNone", ch)
	}
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("user must stay online while another connection is live")
	}
	if ch, _ := store.Disconnect(ctx, 1001, "c-2", "node-0"); ch != ChangeOffline {
		t.Errorf("last disconnect change = %v, want ChangeOffline", ch)
	}
	if on, _ := store.IsOnline(ctx, 1001); on {
		t.Fatal("user must be offline after the last connection closes")
	}
}

func TestStaleDisconnectNeverMarksOffline(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	store.Connect(ctx, 1001, "c-1", "node-0")
	store.Connect(ctx, 1001, "c-2", "node-0")

	// The first connection's teardown runs twice (stale retry). The second
	// SREM must not count against the live set, so the user stays online.
	if ch, _ := store.Disconnect(ctx, 1001, "c-1", "node-0"); ch != ChangeNone {
		t.Fatalf("first cleanup = %v, want ChangeNone", ch)
	}
	if ch, _ := store.Disconnect(ctx, 1001, "c-1", "node-0"); ch != ChangeNone {
		t.Fatalf("stale cleanup = %v, want ChangeNone", ch)
	}
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("user must remain online after a stale disconnect")
	}
	if ch, _ := store.Disconnect(ctx, 1001, "c-2", "node-0"); ch != ChangeOffline {
		t.Errorf("last disconnect = %v, want ChangeOffline", ch)
	}
}

func TestMultiInstanceAggregation(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	// Instance A connects, instance B connects: one transition total, and the
	// user stays online while either instance holds a connection.
	if ch, _ := store.Connect(ctx, 1001, "c-a1", "node-a"); ch != ChangeOnline {
		t.Fatalf("A connect = %v, want ChangeOnline", ch)
	}
	if ch, _ := store.Connect(ctx, 1001, "c-b1", "node-b"); ch != ChangeNone {
		t.Errorf("B connect = %v, want ChangeNone", ch)
	}
	// A's disconnect must NOT flip the user offline while B is still online.
	if ch, _ := store.Disconnect(ctx, 1001, "c-a1", "node-a"); ch != ChangeNone {
		t.Errorf("A disconnect = %v, want ChangeNone", ch)
	}
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("user must stay online while instance B holds a connection")
	}
	if ch, _ := store.Disconnect(ctx, 1001, "c-b1", "node-b"); ch != ChangeOffline {
		t.Errorf("B disconnect = %v, want ChangeOffline", ch)
	}
}

func TestConcurrentConnectSingleTransition(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make(chan Change, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ch, err := store.Connect(ctx, 1001, string(rune('a'+n)), "node-0")
			if err == nil {
				results <- ch
			}
		}(i)
	}
	wg.Wait()
	close(results)
	transitions := 0
	for ch := range results {
		if ch == ChangeOnline {
			transitions++
		}
	}
	if transitions != 1 {
		t.Errorf("online transitions = %d, want exactly 1 (atomic via Lua)", transitions)
	}
}

func TestPresenceExpiry(t *testing.T) {
	store, m := testStore(t, testConfig())
	ctx := context.Background()

	store.Connect(ctx, 1001, "c-1", "node-0")
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("online before expiry")
	}
	// TTL default 60s: fast-forward past it and the presence must expire
	// (a crashed gateway's users are reclaimed).
	m.FastForward(61 * time.Second)
	if on, _ := store.IsOnline(ctx, 1001); on {
		t.Fatal("user must be offline after presence TTL expires")
	}
}

func TestTouchRefreshesPresence(t *testing.T) {
	store, m := testStore(t, testConfig())
	ctx := context.Background()

	store.Connect(ctx, 1001, "c-1", "node-0")
	// Heartbeat sweeper keeps the presence alive across several TTL windows.
	for i := 0; i < 3; i++ {
		m.FastForward(40 * time.Second)
		if err := store.Touch(ctx, 1001, "node-0"); err != nil {
			t.Fatalf("touch: %v", err)
		}
	}
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("presence must survive repeated heartbeats")
	}
}

func TestLastSeenRecordedOnOffline(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	store.Connect(ctx, 1001, "c-1", "node-0")
	if _, err := store.Disconnect(ctx, 1001, "c-1", "node-0"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	last, err := store.GetLastSeen(ctx, 1001)
	if err != nil {
		t.Fatalf("last seen: %v", err)
	}
	if last.IsZero() {
		t.Fatal("last_seen must be recorded on the offline transition")
	}
	// A stale second disconnect must not overwrite/clear anything meaningful.
	store.Disconnect(ctx, 1001, "c-1", "node-0")
	if last2, _ := store.GetLastSeen(ctx, 1001); last2.IsZero() {
		t.Fatal("last_seen must survive a stale disconnect")
	}
}

func TestSetMetaAndConvs(t *testing.T) {
	store, _ := testStore(t, testConfig())
	ctx := context.Background()

	if err := store.SetMeta(ctx, 1001, "busy", "in a meeting"); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	status, custom, err := store.GetMeta(ctx, 1001)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if status != "busy" || custom != "in a meeting" {
		t.Errorf("meta = %q/%q, want busy/in a meeting", status, custom)
	}
	if err := store.ConvsAdd(ctx, 1001, 2001); err != nil {
		t.Fatalf("convs add: %v", err)
	}
	convs, _ := store.Convs(ctx, 1001)
	if len(convs) != 1 || convs[0] != 2001 {
		t.Errorf("convs = %v, want [2001]", convs)
	}
	if err := store.ConvsRemove(ctx, 1001, 2001); err != nil {
		t.Fatalf("convs remove: %v", err)
	}
	if convs, _ := store.Convs(ctx, 1001); len(convs) != 0 {
		t.Errorf("convs after remove = %v, want empty", convs)
	}
}

// recordingNotifier captures presence transitions for service tests.
type recordingNotifier struct {
	mu  sync.Mutex
	evs []ChangeEvent
}

func (n *recordingNotifier) NotifyPresence(_ context.Context, ev ChangeEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evs = append(n.evs, ev)
}

func (n *recordingNotifier) last() (ChangeEvent, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.evs) == 0 {
		return ChangeEvent{}, false
	}
	return n.evs[len(n.evs)-1], true
}

func newTestService(t *testing.T, n Notifier) (*Service, *miniredis.Miniredis) {
	t.Helper()
	store, m := testStore(t, testConfig())
	return NewService(store, testConfig(), n, slog.New(slog.DiscardHandler)), m
}

func TestServiceConnectBroadcastsOnline(t *testing.T) {
	n := &recordingNotifier{}
	svc, _ := newTestService(t, n)
	svc.SetConversation(context.Background(), 1001, 2001)

	svc.Connect(context.Background(), 1001, "c-1")
	ev, ok := n.last()
	if !ok {
		t.Fatal("no presence notification on connect")
	}
	if ev.UserID != 1001 || ev.Status != "online" {
		t.Errorf("ev = %+v, want user 1001 online", ev)
	}
	if len(ev.ConversationIDs) != 1 || ev.ConversationIDs[0] != 2001 {
		t.Errorf("ev convs = %v, want [2001] (fan-out scope)", ev.ConversationIDs)
	}
	if ev.LastSeenAt != nil {
		t.Error("online transition must not carry a last_seen")
	}
}

func TestServiceDisconnectBroadcastsOfflineWithLastSeen(t *testing.T) {
	n := &recordingNotifier{}
	svc, _ := newTestService(t, n)

	svc.Connect(context.Background(), 1001, "c-1")
	svc.Disconnect(context.Background(), 1001, "c-1")
	ev, ok := n.last()
	if !ok {
		t.Fatal("no presence notification on disconnect")
	}
	if ev.Status != "offline" {
		t.Errorf("status = %q, want offline", ev.Status)
	}
	if ev.LastSeenAt == nil || ev.LastSeenAt.IsZero() {
		t.Error("offline transition must carry the authoritative last_seen")
	}
}

func TestServiceStaleDisconnectNoBroadcast(t *testing.T) {
	n := &recordingNotifier{}
	svc, _ := newTestService(t, n)

	svc.Connect(context.Background(), 1001, "c-1")
	svc.Connect(context.Background(), 1001, "c-2")
	svc.Disconnect(context.Background(), 1001, "c-1")
	svc.Disconnect(context.Background(), 1001, "c-1") // stale
	// Only the initial online was broadcast; no offline yet (c-2 still live).
	n.mu.Lock()
	count := len(n.evs)
	n.mu.Unlock()
	if count != 1 {
		t.Errorf("notifications = %d, want 1 (no stale offline)", count)
	}
	if on := svc.IsOnline(context.Background(), 1001); !on {
		t.Fatal("user must still be online")
	}
}

func TestServiceUpdateBroadcastsStatus(t *testing.T) {
	n := &recordingNotifier{}
	svc, _ := newTestService(t, n)

	svc.Update(context.Background(), 1001, "busy", "focus")
	ev, ok := n.last()
	if !ok {
		t.Fatal("no notification on presence.update")
	}
	if ev.Status != "busy" || ev.CustomStatus != "focus" {
		t.Errorf("status = %q/%q, want busy/focus", ev.Status, ev.CustomStatus)
	}
}

func TestSweeperKeepsPresenceAlive(t *testing.T) {
	cfg := testConfig()
	cfg.TTL = 2 * time.Second
	store, _ := testStore(t, cfg)
	ctx := context.Background()

	// Two online users on this instance.
	store.Connect(ctx, 1001, "c-1", "node-0")
	store.Connect(ctx, 1002, "c-2", "node-0")

	hub := &fakeOnlineUsers{users: []int64{1001, 1002}}
	sw := NewSweeper(store, hub, cfg, slog.New(slog.DiscardHandler))

	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = sw.Run(ctx2); close(done) }()

	// Without the sweeper the TTL (2s) would expire the presence; with it the
	// users must remain online across several windows.
	time.Sleep(2500 * time.Millisecond)
	if on, _ := store.IsOnline(ctx, 1001); !on {
		t.Fatal("user 1001 must stay online thanks to the heartbeat sweeper")
	}
	if on, _ := store.IsOnline(ctx, 1002); !on {
		t.Fatal("user 1002 must stay online thanks to the heartbeat sweeper")
	}
	cancel()
	<-done
}

type fakeOnlineUsers struct{ users []int64 }

func (f *fakeOnlineUsers) OnlineUsers() []int64 { return f.users }
