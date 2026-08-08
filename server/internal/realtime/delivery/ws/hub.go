// Package ws is the realtime module's WebSocket transport layer
// (ENGINEERING.md §18). It implements the canonical production hub pattern:
// one read pump + one write pump per connection, a Hub-owned bounded send
// channel per connection, backpressure that drops slow consumers with a
// `4510 Slow Consumer` close (never blocking the fan-out loop), and a
// per-instance `conversationID → set(connID)` registry. No business logic
// lives here — frames are handed to an injected FrameHandler.
package ws

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// Config tunes the transport behavior. Values mirror API.md §16.7 (heartbeat
// & timeouts) and ENGINEERING.md §18.3 (buffer sizing).
type Config struct {
	// SendBufferSize is the bounded per-connection outbound frame buffer
	// (ENGINEERING.md §18.3: 256–1024).
	SendBufferSize int
	// PingInterval is the server-initiated heartbeat cadence (API.md §16.7:
	// 25s).
	PingInterval time.Duration
	// PingTimeout bounds how long a server ping waits for the pong; the socket
	// is dropped after a single missed pong (~55s idle with the default 25s
	// interval, within API.md §16.7's ~60s budget).
	PingTimeout time.Duration
	// WriteTimeout bounds each socket write (write deadlines, §13.3).
	WriteTimeout time.Duration
	// MaxFrameSize caps a single inbound frame (read limit).
	MaxFrameSize int64
}

// DefaultConfig is the production tuning (API.md §16.7, ENGINEERING.md §18.3).
func DefaultConfig() Config {
	return Config{
		SendBufferSize: 256,
		PingInterval:   25 * time.Second,
		PingTimeout:    30 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxFrameSize:   64 << 10,
	}
}

// FrameHandler processes one inbound frame for a connection. It must not block
// the read pump on I/O beyond the frame's own work (ENGINEERING.md §8.4: the
// handler calls the application service; the service publishes; the dispatcher
// delivers — the handler never writes to the socket itself except acks).
type FrameHandler interface {
	// HandleFrame is invoked for every successfully read frame. Returning an
	// error closes the connection with CloseProtocol (4502).
	HandleFrame(ctx context.Context, c *Connection, frame *domain.Frame) error
}

// Hub owns every live connection on this gateway instance and the per-instance
// routing registry (ENGINEERING.md §18.3). It is the stateless dispatch tier:
// the durable truth lives in PG + change_log; Redis is the cross-instance
// backplane; this Hub only routes frames to the local connections subscribed
// to a conversation.
type Hub struct {
	mu      sync.RWMutex
	conns   map[string]*Connection
	byUser  map[int64]map[string]*Connection
	byConv  map[int64]map[string]*Connection
	handler FrameHandler
	log     *slog.Logger
	cfg     Config
	closed  bool
	connSeq atomic.Int64 // connection-id source (API.md §18.1 connection_id)
}

// NewHub builds an empty hub. handler processes inbound frames; log is used for
// connection lifecycle events.
func NewHub(cfg Config, handler FrameHandler, log *slog.Logger) *Hub {
	return &Hub{
		conns:   make(map[string]*Connection),
		byUser:  make(map[int64]map[string]*Connection),
		byConv:  make(map[int64]map[string]*Connection),
		handler: handler,
		log:     log,
		cfg:     cfg,
	}
}

// Config returns the hub's transport configuration.
func (h *Hub) Config() Config { return h.cfg }

// NextConnID mints the next connection id (per-instance monotonic, unique on
// this gateway — the "c-N" form from API.md §18.1).
func (h *Hub) NextConnID() string {
	return fmt.Sprintf("c-%d", h.connSeq.Add(1))
}

// register adds a connection to the registry: by id, by user (for per-user
// fan-out and revocation), and by every conversation it subscribes to.
func (h *Hub) register(c *Connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c.id] = c
	setAdd(h.byUser, c.userID, c.id, c)
	for convID := range c.subscribed {
		setAdd(h.byConv, convID, c.id, c)
	}
}

// unregister removes a connection from every registry index. It is safe to
// call more than once.
func (h *Hub) unregister(c *Connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c.id]; !ok {
		return
	}
	delete(h.conns, c.id)
	if m := h.byUser[c.userID]; m != nil {
		delete(m, c.id)
		if len(m) == 0 {
			delete(h.byUser, c.userID)
		}
	}
	for convID := range c.subscribed {
		setDelete(h.byConv, convID, c.id)
	}
}

// ConnCount returns the number of live connections (metrics).
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Subscribe registers the connection for a conversation's live fan-out. The
// caller (frame handler) must already have verified membership — this method
// is a pure registry update (WS-4: unauthorized subscribes denied upstream).
func (h *Hub) Subscribe(c *Connection, convID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.subscribed[convID] = struct{}{}
	setAdd(h.byConv, convID, c.id, c)
}

// Unsubscribe removes a connection from a conversation's fan-out set.
func (h *Hub) Unsubscribe(c *Connection, convID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(c.subscribed, convID)
	setDelete(h.byConv, convID, c.id)
}

// DeliverToConversation fans out one S2C event to every local connection
// subscribed to the conversation (API.md §18.5–18.8 fan-out). Delivery is
// non-blocking per connection: a slow consumer is dropped with 4510 and
// unsubscribed, but the fan-out loop itself never blocks (ENGINEERING.md
// §18.3).
func (h *Hub) DeliverToConversation(convID int64, typ string, data any) {
	h.mu.RLock()
	conns := make([]*Connection, 0, len(h.byConv[convID]))
	for _, c := range h.byConv[convID] {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.sendEvent(typ, data)
	}
}

// DeliverToUser pushes one S2C event to every connection of a user (receipt
// fan-out to senders, §18.9/18.10, session revocation, §18.19).
func (h *Hub) DeliverToUser(userID int64, typ string, data any) {
	h.mu.RLock()
	conns := make([]*Connection, 0, len(h.byUser[userID]))
	for _, c := range h.byUser[userID] {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.sendEvent(typ, data)
	}
}

// CloseSession force-closes every connection bound to a session (session
// revocation, §18.19/§18.23 code 4403).
func (h *Hub) CloseSession(sessionID int64) {
	h.mu.RLock()
	var conns []*Connection
	for _, c := range h.conns {
		if c.sessionID == sessionID {
			conns = append(conns, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.closeWith(domain.CloseSessionRevoked)
	}
}

// CloseUser force-closes every connection of a user (logout-all, admin
// suspension).
func (h *Hub) CloseUser(userID int64) {
	h.mu.RLock()
	conns := make([]*Connection, 0, len(h.byUser[userID]))
	for _, c := range h.byUser[userID] {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.closeWith(domain.CloseSessionRevoked)
	}
}

// Shutdown gracefully drains the gateway: it broadcasts server.shutdown to
// every connection, waits up to drain for in-flight writes, then force-closes
// (API.md §18.21, ENGINEERING.md §18.3, WS-8). Shutdown must be called exactly
// once, from the process lifecycle, and it returns after all connections are
// closed.
func (h *Hub) Shutdown(ctx context.Context, drain time.Duration) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	conns := make([]*Connection, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	for _, c := range conns {
		c.sendEvent(domain.EventServerShutdown, map[string]any{
			"reason":         "maintenance",
			"retry_after_ms": 15000,
		})
	}

	// Give the write pumps a moment to flush, then force-close.
	deadline := time.Now().Add(drain)
	for _, c := range conns {
		remain := time.Until(deadline)
		if remain <= 0 {
			c.closeWith(domain.CloseServerRestart)
			continue
		}
		select {
		case <-c.done:
		case <-time.After(remain):
			c.closeWith(domain.CloseServerRestart)
		}
	}
	// Guarantee: every connection is torn down even if a pump is stuck.
	for _, c := range conns {
		c.closeNow()
	}
}

func setAdd(m map[int64]map[string]*Connection, key int64, id string, c *Connection) {
	s, ok := m[key]
	if !ok {
		s = make(map[string]*Connection)
		m[key] = s
	}
	s[id] = c
}

func setDelete(m map[int64]map[string]*Connection, key int64, id string) {
	if s, ok := m[key]; ok {
		delete(s, id)
		if len(s) == 0 {
			delete(m, key)
		}
	}
}
