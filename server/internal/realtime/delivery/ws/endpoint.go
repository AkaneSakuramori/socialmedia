package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	authdomain "github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// ClaimsAuthenticator authenticates an access token and returns the validated
// claim set. The auth application service implements it (AuthenticateClaims);
// the endpoint never re-implements token logic (SECURITY_SPEC.md JWT-5).
type ClaimsAuthenticator interface {
	AuthenticateClaims(ctx context.Context, token, deviceID string) (*userdomain.User, *authdomain.AccessClaims, error)
}

// HeadSource reports the change_log head for hello_ack/resume cursors
// (API.md §18.1 global_seq). The chat module's change-log repository implements
// it; it is optional (best-effort) for the handshake.
type HeadSource interface {
	Head(ctx context.Context) (int64, error)
}

// Endpoint upgrades HTTP to a WebSocket and binds every accepted socket to the
// authenticated (user_id, session_id) (API.md §16.1, ENGINEERING.md §18.2).
// It is the single WS gateway entry point mounted at GET /v1/ws.
//
// Auth order: a query ?access_token= (+ ?device_id=) authenticates at upgrade;
// otherwise the first frame must be hello carrying the token. Both paths run
// the same AuthenticateClaims and bind the same principal.
type Endpoint struct {
	hub  *Hub
	auth ClaimsAuthenticator
	head HeadSource
	log  *slog.Logger
	// HandshakeTimeout bounds how long the endpoint waits for the first frame
	// (the hello that carries the token when none was in the query).
	HandshakeTimeout time.Duration
	// OriginPatterns is the allowed CORS origins for cross-origin sockets;
	// empty allows the request's own host (websocket.Accept default).
	OriginPatterns []string

	// presence/typing drive the ephemeral connection lifecycle (optional).
	presence *presence.Service
	typing   *typing.Service
}

// NewEndpoint builds the WS gateway endpoint.
func NewEndpoint(hub *Hub, auth ClaimsAuthenticator, head HeadSource, log *slog.Logger) *Endpoint {
	return &Endpoint{
		hub:              hub,
		auth:             auth,
		head:             head,
		log:              log,
		HandshakeTimeout: 10 * time.Second,
	}
}

// WithPresence wires the ephemeral presence service (connection lifecycle).
func (e *Endpoint) WithPresence(p *presence.Service) *Endpoint {
	e.presence = p
	return e
}

// WithTyping wires the ephemeral typing service (disconnect cleanup).
func (e *Endpoint) WithTyping(t *typing.Service) *Endpoint {
	e.typing = t
	return e
}

// ServeHTTP performs the upgrade, authenticates, binds the connection, sends
// hello_ack, and runs the socket pumps for the request's lifetime.
func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:   []string{domain.Subprotocol},
		OriginPatterns: e.OriginPatterns,
	})
	if err != nil {
		return
	}

	// The first frame is always hello (API.md §17.1): it carries the access
	// token when the query does not, and the bootstrap cursors. Reading it here
	// unifies the query-token and first-frame auth paths (§16.1) and guarantees
	// hello_ack is emitted exactly once.
	hctx, cancel := context.WithTimeout(r.Context(), e.HandshakeTimeout)
	defer cancel()
	hello, err := e.readHello(hctx, ws)
	if err != nil {
		e.log.Debug("realtime: handshake read failed", "error", err)
		_ = ws.Close(websocket.StatusCode(domain.CloseProtocol), "invalid handshake")
		return
	}

	// Authenticate before any payload: query token takes precedence, then the
	// hello's token (§16.1).
	_, claims, err := e.authenticate(r, hello)
	if err != nil {
		e.log.Debug("realtime: handshake auth rejected", "error", err)
		e.reject(ws, err)
		return
	}

	conn := newConnection(e.hub.NextConnID(), claims.UserID, claims.SessionID, claims.DeviceID, ws, e.hub)
	e.hub.register(conn)

	// Connection lifecycle: register presence (online) and bind disconnect
	// cleanup (presence offline + typing cleanup) to the connection teardown.
	if e.presence != nil {
		uid, cid := conn.UserID(), conn.ID()
		ectx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e.presence.Connect(ectx, uid, cid)
		cancel()
		conn.onClose = func() {
			ectx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			offline := e.presence.Disconnect(ectx, uid, cid)
			// Typing state is per user: only clear it once every connection is
			// gone, so another live device's typing survives a single disconnect.
			if offline && e.typing != nil {
				e.typing.CleanupUser(ectx, uid)
			}
		}
	}

	conn.sendEvent(domain.EventHelloAck, map[string]any{
		"connection_id": conn.ID(),
		"session_id":    conn.SessionID(),
		"server_time":   time.Now().UTC().Format(time.RFC3339),
		"last_seq":      int64(0),
		"global_seq":    e.headGlobalSeq(r.Context(), conn),
	})

	e.log.Debug("realtime: connection open", "conn", conn.ID(), "user", claims.UserID, "session", claims.SessionID)
	conn.Run(r.Context())
	e.log.Debug("realtime: connection closed", "conn", conn.ID(), "user", claims.UserID)
}

// readHello reads and decodes the mandatory first hello frame.
func (e *Endpoint) readHello(ctx context.Context, ws *websocket.Conn) (*helloPayload, error) {
	typ, b, err := ws.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("realtime: read hello: %w", err)
	}
	if typ != websocket.MessageText {
		return nil, errors.New("realtime: hello must be a text frame")
	}
	frame, err := domain.Decode(b)
	if err != nil {
		return nil, err
	}
	if frame.Type != domain.EventHello {
		return nil, errors.New("realtime: first frame must be hello")
	}
	var p helloPayload
	if err := json.Unmarshal(frame.Data, &p); err != nil {
		return nil, fmt.Errorf("realtime: hello payload: %w", err)
	}
	return &p, nil
}

// authenticate resolves the token from the query string or the hello frame,
// validates it, and returns the bound principal + claims.
func (e *Endpoint) authenticate(r *http.Request, hello *helloPayload) (*userdomain.User, *authdomain.AccessClaims, error) {
	token := r.URL.Query().Get("access_token")
	deviceID := r.URL.Query().Get("device_id")
	if token == "" {
		token = hello.AccessToken
		deviceID = hello.DeviceID
	}
	if token == "" {
		return nil, nil, errors.New("realtime: hello missing access_token")
	}
	if deviceID == "" {
		return nil, nil, errors.New("realtime: hello missing device_id")
	}
	return e.auth.AuthenticateClaims(r.Context(), token, deviceID)
}

// headGlobalSeq resolves the change_log head for hello_ack. It is best-effort:
// on a transient DB error the field is omitted rather than failing the
// handshake (the client falls back to sync).
func (e *Endpoint) headGlobalSeq(ctx context.Context, conn *Connection) int64 {
	if e.head == nil {
		return 0
	}
	if v, err := e.head.Head(ctx); err == nil {
		return v
	} else {
		e.log.Debug("realtime: change_log head unavailable", "conn", conn.ID(), "error", err)
	}
	return 0
}

// reject closes a failed handshake with an error frame and the appropriate
// close code (API.md §18.23): 4401 auth/token invalid, 4403 session revoked.
func (e *Endpoint) reject(ws *websocket.Conn, err error) {
	code := domain.CloseAuthInvalid
	switch {
	case errors.Is(err, authdomain.ErrSessionRevoked):
		code = domain.CloseSessionRevoked
	case errors.Is(err, authdomain.ErrAccountSuspended), errors.Is(err, authdomain.ErrAccountDeleted):
		code = domain.CloseSessionRevoked
	}
	// Best-effort close; the close code is the authoritative signal.
	_ = ws.Close(websocket.StatusCode(code), err.Error())
}
