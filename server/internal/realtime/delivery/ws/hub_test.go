package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// testHandler is a no-op FrameHandler for hub-only tests.
type testHandler struct{}

func (testHandler) HandleFrame(context.Context, *Connection, *domain.Frame) error { return nil }

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	return NewHub(DefaultConfig(), testHandler{}, observability.NewLogger("test"))
}

// fakeSocket is a minimal socket double for connection tests that exercise the
// hub registry without real network I/O.
type fakeSocket struct {
	written chan []byte
	closed  chan struct{}
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{written: make(chan []byte, 32), closed: make(chan struct{})}
}

func (f *fakeSocket) Write(_ context.Context, _ websocket.MessageType, p []byte) error {
	select {
	case f.written <- p:
	default:
	}
	return nil
}

func (f *fakeSocket) CloseNow() error { close(f.closed); return nil }

// realConn builds a real server-side websocket registered into h so pumps that
// touch the socket (CloseNow, Write) behave like production. The client side is
// kept open until the test ends to avoid an unexpected EOF during the test.
func realConn(t *testing.T, h *Hub, id string, userID, sessionID int64) *Connection {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		got <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.Dial(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "test done") })
	s := <-got

	c := newConnection(id, userID, sessionID, "d-1", s, h)
	h.register(c)
	return c
}

// newRegisteredConn builds a connection and registers it with the hub without
// running its pumps (so tests can call hub methods directly). The socket is a
// stub that does not support socket I/O; use realConn for socket-touching
// tests.
func newRegisteredConn(h *Hub, id string, userID, sessionID int64) *Connection {
	c := newConnection(id, userID, sessionID, "d-1", &websocket.Conn{}, h)
	h.register(c)
	return c
}

func TestHubSubscribeAndDeliverToConversation(t *testing.T) {
	h := newTestHub(t)
	// Keep a real-ish connection: register a subscription directly.
	alice := newRegisteredConn(h, "c-1", 1001, 7001)
	bob := newRegisteredConn(h, "c-2", 1002, 7002)
	h.Subscribe(alice, 2001)
	h.Subscribe(bob, 2001)

	// Both receive the fan-out.
	h.DeliverToConversation(2001, domain.EventMessageCreated, map[string]any{"message_id": 1})
	if got := alice.seq; got != 1 {
		t.Errorf("alice seq = %d, want 1 (per-connection monotonic)", got)
	}
	if got := bob.seq; got != 1 {
		t.Errorf("bob seq = %d, want 1", got)
	}

	// A conversation nobody subscribed to fans out to nobody.
	h.DeliverToConversation(9999, domain.EventMessageCreated, map[string]any{})
	if got := alice.seq; got != 1 {
		t.Errorf("alice seq = %d, want 1 (unrelated conversation must not deliver)", got)
	}

	// After unsubscribe, bob no longer receives.
	h.Unsubscribe(bob, 2001)
	h.DeliverToConversation(2001, domain.EventMessageCreated, map[string]any{"message_id": 2})
	if got := alice.seq; got != 2 {
		t.Errorf("alice seq = %d, want 2", got)
	}
	if got := bob.seq; got != 1 {
		t.Errorf("bob seq = %d, want 1 (unsubscribed)", got)
	}
}

func TestHubUnregisterCleansAllIndexes(t *testing.T) {
	h := newTestHub(t)
	alice := newRegisteredConn(h, "c-1", 1001, 7001)
	h.Subscribe(alice, 2001)
	h.Subscribe(alice, 2002)
	h.unregister(alice)

	if got := h.ConnCount(); got != 0 {
		t.Errorf("ConnCount = %d, want 0", got)
	}
	if _, ok := h.byUser[1001]; ok {
		t.Error("byUser index not cleaned")
	}
	if _, ok := h.byConv[2001]; ok {
		t.Error("byConv index not cleaned for 2001")
	}
	if _, ok := h.byConv[2002]; ok {
		t.Error("byConv index not cleaned for 2002")
	}
}

func TestHubDeliverToUser(t *testing.T) {
	h := newTestHub(t)
	aliceA := newRegisteredConn(h, "c-1", 1001, 7001)
	aliceB := newRegisteredConn(h, "c-2", 1001, 7002)
	bob := newRegisteredConn(h, "c-3", 1002, 7003)

	h.DeliverToUser(1001, domain.EventServerReceiptRead, map[string]any{})
	if aliceA.seq != 1 || aliceB.seq != 1 {
		t.Errorf("alice conns seq = %d/%d, want 1/1 (multi-connection fan-out)", aliceA.seq, aliceB.seq)
	}
	if bob.seq != 0 {
		t.Errorf("bob seq = %d, want 0 (not addressed)", bob.seq)
	}
}

func TestHubCloseSessionClosesMatchingConnection(t *testing.T) {
	h := newTestHub(t)
	same := newRegisteredConn(h, "c-1", 1001, 7001)
	other := newRegisteredConn(h, "c-2", 1001, 7002)

	h.CloseSession(7001)
	if !same.isClosing() {
		t.Error("connection bound to revoked session must be closing (4403)")
	}
	if other.isClosing() {
		t.Error("connection of a different session must stay open")
	}
}

func TestHubCloseUserClosesEveryConnection(t *testing.T) {
	h := newTestHub(t)
	a := newRegisteredConn(h, "c-1", 1001, 7001)
	b := newRegisteredConn(h, "c-2", 1001, 7002)
	c := newRegisteredConn(h, "c-3", 1002, 7003)

	h.CloseUser(1001)
	if !a.isClosing() || !b.isClosing() {
		t.Error("all connections of the user must be closed")
	}
	if c.isClosing() {
		t.Error("another user's connection must stay open")
	}
}

func TestHubShutdownBroadcastsServerShutdown(t *testing.T) {
	h := newTestHub(t)
	conn := realConn(t, h, "c-1", 1001, 7001)
	// Replace the send channel with a buffered one we can inspect without
	// running pumps; the event is still enqueued through sendEvent.
	h.mu.Lock()
	conn.send = make(chan []byte, 8)
	h.mu.Unlock()

	h.Shutdown(context.Background(), 50*time.Millisecond)

	select {
	case b := <-conn.send:
		f, err := domain.Decode(b)
		if err != nil {
			t.Fatalf("shutdown frame decode: %v", err)
		}
		if f.Type != domain.EventServerShutdown {
			t.Errorf("frame type = %q, want server.shutdown", f.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("server.shutdown frame never enqueued")
	}
	if !h.closed {
		t.Error("hub must be marked closed after Shutdown")
	}
}

func TestHubShutdownIdempotent(t *testing.T) {
	h := newTestHub(t)
	realConn(t, h, "c-1", 1001, 7001)
	h.Shutdown(context.Background(), 10*time.Millisecond)
	h.Shutdown(context.Background(), 10*time.Millisecond) // must not panic
}

func TestConnectionSeqMonotonicUnderConcurrency(t *testing.T) {
	h := newTestHub(t)
	conn := newRegisteredConn(h, "c-1", 1001, 7001)
	h.mu.Lock()
	conn.send = make(chan []byte, 8192) // large enough to never trigger the slow-consumer path
	h.mu.Unlock()

	const workers = 8
	const perWorker = 500
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < perWorker; j++ {
				conn.sendEvent(domain.EventMessageCreated, map[string]any{})
			}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	if got := conn.seq; got != workers*perWorker {
		t.Errorf("seq = %d, want %d (no lost/duplicated sequences)", got, workers*perWorker)
	}
}
