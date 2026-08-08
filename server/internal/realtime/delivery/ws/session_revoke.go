package ws

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// sessionRevokeChannel is the Redis channel a session-revocation publisher
// pushes to (channel per session id — a pub/sub fan-out, not a value).
// SECURITY_SPEC.md §11, API.md §4.5/§18.19: revoking a session closes its
// sockets so a logged-out device cannot keep receiving messages.
const sessionRevokeChannel = "sessions:revoke"

// SessionRevokeWatcher subscribes to the session-revocation stream and force-
// closes every socket bound to a revoked session (code 4403). It is a
// fire-and-forget signal: if a message is missed the socket is eventually
// killed by the next gateway check; delivery is best-effort (ENGINEERING.md
// §18.3).
type SessionRevokeWatcher struct {
	hub *Hub
	log *slog.Logger
}

// NewSessionRevokeWatcher builds the watcher over a shared hub.
func NewSessionRevokeWatcher(hub *Hub, log *slog.Logger) *SessionRevokeWatcher {
	return &SessionRevokeWatcher{hub: hub, log: log}
}

// Run subscribes and forwards revocations until ctx is cancelled. It blocks;
// callers run it as a goroutine and cancel ctx to stop.
func (w *SessionRevokeWatcher) Run(ctx context.Context, client *redis.Client) error {
	sub := client.Subscribe(ctx, sessionRevokeChannel)
	ch := sub.Channel()
	defer func() { _ = sub.Close() }()

	w.log.Info("realtime: session-revoke watcher subscribed", "channel", sessionRevokeChannel)
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return fmt.Errorf("realtime: session-revoke channel closed")
			}
			sessionID, err := strconv.ParseInt(msg.Payload, 10, 64)
			if err != nil || sessionID <= 0 {
				w.log.Warn("realtime: malformed session-revoke payload", "payload", msg.Payload)
				continue
			}
			w.log.Info("realtime: session revoked, closing sockets", "session_id", sessionID)
			w.hub.CloseSession(sessionID)
		}
	}
}
