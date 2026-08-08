package ws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// recordingHandler captures frames and optionally returns an error to force a
// close.
type recordingHandler struct {
	got      chan *domain.Frame
	reject   func(*domain.Frame) error
	lastConn *Connection
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{got: make(chan *domain.Frame, 64)}
}

func (h *recordingHandler) HandleFrame(_ context.Context, c *Connection, f *domain.Frame) error {
	h.lastConn = c
	if h.reject != nil {
		if err := h.reject(f); err != nil {
			return err
		}
	}
	h.got <- f
	return nil
}

// startTestServer accepts one websocket, builds a Connection, and runs its
// pumps. It returns the server hub, the client socket, and the recording
// handler. The returned hub is guaranteed to have the connection registered
// before it returns, so tests cannot deliver before fan-out is wired up.
func startTestServer(t *testing.T, cfg Config, h *recordingHandler) (*Hub, *websocket.Conn) {
	t.Helper()
	if cfg.SendBufferSize == 0 {
		cfg = DefaultConfig()
	}
	hub := NewHub(cfg, h, observability.NewLogger("test"))

	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{domain.Subprotocol}})
		if err != nil {
			return
		}
		c := newConnection("conn-1", 1001, 7001, "d-1", ws, hub)
		hub.register(c)
		close(ready)
		// NB: use a fresh context, not r.Context() — the request context is
		// cancelled the moment this handler returns.
		go func() {
			defer hub.unregister(c)
			c.Run(context.Background())
		}()
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.Dial(context.Background(), srv.URL, &websocket.DialOptions{
		Subprotocols: []string{domain.Subprotocol},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.CloseNow() })
	<-ready // connection registered: fan-out can no longer miss it
	return hub, client
}

func readFrame(t *testing.T, c *websocket.Conn) *domain.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := domain.Decode(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f
}

func TestConnectionReceivesFannedOutEvent(t *testing.T) {
	hub, client := startTestServer(t, DefaultConfig(), newRecordingHandler())
	defer hub.Shutdown(context.Background(), 200*time.Millisecond)

	hub.DeliverToUser(1001, domain.EventServerReceiptRead, map[string]any{"message_id": 9})

	f := readFrame(t, client)
	if f.Type != domain.EventServerReceiptRead {
		t.Fatalf("type = %q, want receipt.read", f.Type)
	}
	if f.Seq == nil || *f.Seq != 1 {
		t.Errorf("seq = %v, want 1", f.Seq)
	}
}

func TestConnectionForwardsInboundFrame(t *testing.T) {
	h := newRecordingHandler()
	hub, client := startTestServer(t, DefaultConfig(), h)
	defer hub.Shutdown(context.Background(), 200*time.Millisecond)

	// Client sends a subscribe frame; handler records it.
	_ = client.Write(context.Background(), websocket.MessageText, []byte(`{"v":1,"id":"op-1","type":"subscribe","data":{"conversation_id":2001}}`))
	select {
	case f := <-h.got:
		if f.Type != domain.EventSubscribe {
			t.Errorf("type = %q, want subscribe", f.Type)
		}
		if f.ID != "op-1" {
			t.Errorf("id = %q, want op-1", f.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler never received inbound frame")
	}
}

func TestConnectionRejectingHandlerClosesSocket(t *testing.T) {
	h := newRecordingHandler()
	h.reject = func(f *domain.Frame) error {
		if f.Type == domain.EventSubscribe {
			return errors.New("no access")
		}
		return nil
	}
	hub, client := startTestServer(t, DefaultConfig(), h)
	defer hub.Shutdown(context.Background(), 200*time.Millisecond)

	_ = client.Write(context.Background(), websocket.MessageText, []byte(`{"v":1,"id":"op-1","type":"subscribe"}`))
	// Handler rejection closes the socket with 4502.
	select {
	case <-h.got:
		t.Fatal("rejected frame must not be delivered")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err := client.Read(ctx)
	if err == nil {
		t.Fatal("expected connection close after handler rejection")
	}
	if websocket.CloseStatus(err) != websocket.StatusCode(domain.CloseProtocol) {
		t.Errorf("close status = %v, want 4502", websocket.CloseStatus(err))
	}
}

func TestConnectionSlowConsumerDroppedWith4510(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SendBufferSize = 2
	hub, _ := startTestServer(t, cfg, newRecordingHandler())
	defer hub.Shutdown(context.Background(), 200*time.Millisecond)

	// Fill the send buffer; the write pump is stuck because the client never
	// reads. Further fan-out must drop the connection with 4510 instead of
	// blocking the hub.
	for i := 0; i < 2000; i++ {
		hub.DeliverToUser(1001, domain.EventMessageCreated, map[string]any{"n": i})
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ConnCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hub.ConnCount() != 0 {
		t.Fatal("slow consumer connection was not removed")
	}
}

func TestConnectionPingHeartbeatKeepsAlive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	cfg.PingTimeout = 100 * time.Millisecond
	hub, client := startTestServer(t, cfg, newRecordingHandler())
	defer hub.Shutdown(context.Background(), 200*time.Millisecond)

	// The coder/websocket client auto-responds to pings only while reading;
	// keep a draining reader so the server's heartbeat always completes.
	stop := make(chan struct{})
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _, err := client.Read(context.Background())
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		_ = client.CloseNow() // unblocks the draining reader
		close(stop)
		<-clientDone
	}()

	// The connection must survive multiple heartbeat intervals.
	time.Sleep(300 * time.Millisecond)
	if hub.ConnCount() != 1 {
		t.Fatalf("ConnCount = %d, want 1 (heartbeat should keep the socket alive)", hub.ConnCount())
	}
}

func TestConnectionHeartbeatLossDropsSocket(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	cfg.PingTimeout = 100 * time.Millisecond
	hub, client := startTestServer(t, cfg, newRecordingHandler())
	defer func() { _ = client.CloseNow() }()

	// The client never reads, so it never responds to the server's pings. The
	// server's Ping must time out and the connection must be dropped.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ConnCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hub.ConnCount() != 0 {
		t.Fatal("connection with lost heartbeat was not dropped")
	}
}

func TestConnectionSeqContinuesAcrossFrames(t *testing.T) {
	hub, client := startTestServer(t, DefaultConfig(), newRecordingHandler())
	defer hub.Shutdown(context.Background(), 200*time.Millisecond)

	hub.DeliverToUser(1001, domain.EventMessageCreated, map[string]any{"m": 1})
	hub.DeliverToUser(1001, domain.EventMessageCreated, map[string]any{"m": 2})
	f1 := readFrame(t, client)
	f2 := readFrame(t, client)
	if f1.Seq == nil || f2.Seq == nil || *f1.Seq != 1 || *f2.Seq != 2 {
		t.Errorf("seqs = %v/%v, want 1/2", f1.Seq, f2.Seq)
	}
}
