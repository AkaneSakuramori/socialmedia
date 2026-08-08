package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// Connection is one live socket. It runs exactly two goroutines:
//
//   - readPump: reads frames, forwards each to the hub's FrameHandler.
//   - writePump: drains the bounded send channel, serializing every socket
//     write so concurrent fan-out can never interleave frames (ENGINEERING.md
//     §18.3), and drives the server-initiated heartbeat.
//
// All S2C traffic flows through sendEvent → the send channel, so there is a
// single writer to the socket at any moment.
type Connection struct {
	id        string
	userID    int64
	sessionID int64
	deviceID  string

	hub        *Hub
	ws         *websocket.Conn
	send       chan []byte
	subscribed map[int64]struct{} // guarded by hub.mu

	seqMu sync.Mutex
	seq   int64 // per-connection monotonic S2C seq (API.md §16.2)

	done      chan struct{}
	closingCh chan struct{} // closed once a close is requested; wakes the write pump
	closeOnce sync.Once
	closeCode domain.CloseCode
	closing   atomic.Bool

	// rate holds the per-connection WS frame budget enforcement (API.md §16.8,
	// SECURITY_SPEC.md WS-3) and the sustained-abuse counter.
	rate *connRate
}

// newConnection wires a socket into the hub without registering it. The caller
// must call Register (via hub.register) and then Run to start the pumps.
func newConnection(id string, userID, sessionID int64, deviceID string, ws *websocket.Conn, hub *Hub) *Connection {
	return &Connection{
		id:         id,
		userID:     userID,
		sessionID:  sessionID,
		deviceID:   deviceID,
		hub:        hub,
		ws:         ws,
		send:       make(chan []byte, hub.cfg.SendBufferSize),
		subscribed: make(map[int64]struct{}),
		done:       make(chan struct{}),
		closingCh:  make(chan struct{}),
		rate:       newConnRate(),
	}
}

// ID returns the connection id.
func (c *Connection) ID() string { return c.id }

// UserID returns the authenticated user bound to this socket.
func (c *Connection) UserID() int64 { return c.userID }

// SessionID returns the authenticated session bound to this socket.
func (c *Connection) SessionID() int64 { return c.sessionID }

// DeviceID returns the authenticated device bound to this socket.
func (c *Connection) DeviceID() string { return c.deviceID }

// isBound reports whether the socket is authenticated (user + session bound).
// The endpoint binds before pumps start; an unbound socket may only receive the
// hello frame that carries the access token (API.md §16.1).
func (c *Connection) isBound() bool { return c.userID > 0 && c.sessionID > 0 }

// Subscribe registers this connection for a conversation's fan-out (membership
// must already be verified by the frame handler).
func (c *Connection) Subscribe(convID int64) { c.hub.Subscribe(c, convID) }

// Unsubscribe removes this connection from a conversation's fan-out.
func (c *Connection) Unsubscribe(convID int64) { c.hub.Unsubscribe(c, convID) }

// Run starts the read and write pumps and blocks until the connection closes.
// The hub's unregister runs once both pumps have exited.
func (c *Connection) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.readPump(ctx)
	}()
	go func() {
		defer wg.Done()
		c.writePump(ctx)
	}()
	wg.Wait()
	c.hub.unregister(c)
	close(c.done)
	c.ws.CloseNow()
}

// Close requests a graceful close with the given code; the write pump performs
// the actual close frame. It is idempotent.
func (c *Connection) Close(code domain.CloseCode) {
	c.closeWith(code)
}

func (c *Connection) closeWith(code domain.CloseCode) {
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closing.Store(true)
		close(c.closingCh)
	})
}

// drop aborts the socket immediately (no close frame). It is used for
// unresponsive peers (heartbeat loss) and slow consumers, where a graceful
// handshake would block on the same dead peer.
func (c *Connection) drop() {
	c.ws.CloseNow()
}

// closeNow force-closes the underlying socket regardless of pump state. It is
// the last-resort teardown used by Hub.Shutdown.
func (c *Connection) closeNow() { c.drop() }

// isClosing reports whether a close has been requested.
func (c *Connection) isClosing() bool { return c.closing.Load() }

// sendEvent stamps the per-connection seq and enqueues an S2C frame. If the
// send buffer is full the connection is a slow consumer: it is dropped (4510)
// so the fan-out loop is never blocked (ENGINEERING.md §18.3). It returns false
// when the frame was dropped (connection closing or slow consumer).
func (c *Connection) sendEvent(typ string, data any) bool {
	frame := domain.Frame{
		Version: domain.ProtocolVersion,
		Type:    typ,
		At:      time.Now().UTC(),
	}
	c.seqMu.Lock()
	c.seq++
	seq := c.seq
	c.seqMu.Unlock()
	frame.Seq = &seq

	payload, err := json.Marshal(data)
	if err != nil {
		c.hub.log.Error("realtime: marshal event data", "type", typ, "error", err)
		return false
	}
	frame.Data = payload

	b, err := frame.Encode()
	if err != nil {
		c.hub.log.Error("realtime: encode frame", "type", typ, "error", err)
		return false
	}

	if c.isClosing() {
		return false
	}
	select {
	case c.send <- b:
		return true
	default:
		// Slow consumer: drop with 4510 and hand off to resume (API.md §18.3).
		// The write pump may be blocked on this very socket, so abort it here to
		// unblock the pump; the close code is recorded for observability.
		c.hub.log.Warn("realtime: slow consumer dropped", "conn", c.id, "user", c.userID)
		c.closeWith(domain.CloseSlowConsumer)
		c.drop()
		return false
	}
}

// ack writes an ack frame directly (API.md §18.3). Acks are small and are the
// one frame the handler is allowed to send; they still go through the send
// channel to preserve single-writer ordering.
func (c *Connection) ack(id string, result any, errCode string) {
	var data map[string]any
	if errCode == "" {
		data = map[string]any{"id": id, "result": result}
	} else {
		data = map[string]any{"id": id, "error": map[string]any{"code": errCode}}
	}
	c.sendEvent(domain.EventServerAck, data)
}

// readPump reads frames, applies the read limit, and forwards each frame to the
// handler. Liveness is the heartbeat's job (Ping concurrent with this reader),
// so Read blocks on the connection context rather than a fixed read deadline —
// otherwise a healthy idle connection would be reaped (coder/websocket consumes
// pongs internally without resetting a per-Read deadline). On any terminal
// error it requests a graceful close.
func (c *Connection) readPump(ctx context.Context) {
	c.ws.SetReadLimit(c.hub.cfg.MaxFrameSize)
	for {
		if c.isClosing() {
			return
		}
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure &&
				websocket.CloseStatus(err) != websocket.StatusGoingAway {
				c.hub.log.Debug("realtime: read pump exit", "conn", c.id, "error", err)
			}
			c.closeWith(domain.CloseProtocol)
			return
		}
		if typ != websocket.MessageText {
			// Protocol violation: only text frames carry the JSON envelope.
			c.closeWith(domain.CloseProtocol)
			return
		}
		frame, err := domain.Decode(b)
		if err != nil {
			c.hub.log.Debug("realtime: undecodable frame", "conn", c.id, "error", err)
			c.closeWith(domain.CloseProtocol)
			return
		}
		if err := c.hub.handler.HandleFrame(ctx, c, frame); err != nil {
			c.hub.log.Debug("realtime: frame handler rejected", "conn", c.id, "error", err)
			c.closeWith(domain.CloseProtocol)
			return
		}
	}
}

// writePump drains the send channel with write deadlines and runs the
// server-initiated heartbeat (API.md §16.7): ping every interval, drop the
// socket after PingTimeout with no pong.
func (c *Connection) writePump(ctx context.Context) {
	ticker := time.NewTicker(c.hub.cfg.PingInterval)
	defer ticker.Stop()
	for {
		if c.isClosing() {
			// Drain the send channel before closing so acks enqueued ahead of
			// the close (e.g. RATE_LIMITED, API.md §16.8) reach the client.
			c.drain()
			c.gracefulClose()
			return
		}
		select {
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, c.hub.cfg.WriteTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.hub.log.Debug("realtime: write pump exit", "conn", c.id, "error", err)
				c.drop()
				return
			}
		case <-ticker.C:
			if err := c.heartbeat(ctx); err != nil {
				c.hub.log.Debug("realtime: heartbeat lost", "conn", c.id, "error", err)
				c.closeWith(domain.CloseProtocol)
				c.drop()
				return
			}
		case <-c.closingCh:
			c.drain()
			c.gracefulClose()
			return
		case <-ctx.Done():
			return
		case <-c.done:
			return
		}
	}
}

// drain flushes the send channel best-effort before a graceful close. It is
// bounded by WriteTimeout per frame so a stuck socket cannot stall shutdown.
func (c *Connection) drain() {
	for {
		select {
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(context.Background(), c.hub.cfg.WriteTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		default:
			return
		}
	}
}

// gracefulClose sends a close frame with the recorded code. It is the write
// pump's sole writer, so it may touch the socket directly. Close's handshake is
// bounded internally (5s), and on failure the socket is aborted.
func (c *Connection) gracefulClose() {
	code := websocket.StatusCode(c.closeCode)
	if code == 0 {
		code = websocket.StatusNormalClosure
	}
	if err := c.ws.Close(code, c.closeCode.String()); err != nil {
		c.drop()
	}
}

// heartbeat sends one server ping and waits for the pong with PingTimeout. The
// read pump is the concurrent Reader that completes the pong (coder/websocket
// requires Ping to be concurrent with Reader).
func (c *Connection) heartbeat(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, c.hub.cfg.PingTimeout)
	defer cancel()
	return c.ws.Ping(pctx)
}

// String returns a compact identity for logging.
func (c *Connection) String() string {
	return fmt.Sprintf("conn=%s user=%d session=%d device=%s", c.id, c.userID, c.sessionID, c.deviceID)
}
