package presence

import (
	"context"
	"log/slog"
	"time"
)

// Change is defined in store.go: the transition outcome of a lifecycle op.

// ChangeEvent is the presence transition a Service reports to its notifier
// (API.md §18.12). ConversationIDs are the user's conversation-interest set at
// transition time, used to scope the presence.changed fan-out (ARCHITECTURE.md
// §15.2).
type ChangeEvent struct {
	UserID          int64
	Status          string // online | offline | away | busy
	CustomStatus    string
	LastSeenAt      *time.Time
	ConversationIDs []int64
}

// Notifier receives presence transitions. The delivery layer implements it by
// publishing presence.changed events onto the Redis backplane so every gateway
// instance (including this one) fans them out to the affected conversations'
// local sockets. It must not block the presence hot path for long; a short
// publish timeout is expected.
type Notifier interface {
	NotifyPresence(ctx context.Context, ev ChangeEvent)
}

// Service orchestrates connection lifecycle, aggregation, last-seen, and
// transition fan-out. It is the only way the realtime layer touches presence;
// the store is injected so unit tests run without Redis.
type Service struct {
	store    Store
	cfg      Config
	log      *slog.Logger
	notifier Notifier // optional (nil → no fan-out)
	now      func() time.Time
}

// NewService builds the presence service. notifier may be nil (presence still
// tracked, just not broadcast).
func NewService(store Store, cfg Config, notifier Notifier, log *slog.Logger) *Service {
	return &Service{store: store, cfg: cfg, log: log, notifier: notifier, now: time.Now}
}

// Connect registers a live connection and broadcasts the online transition
// when the user was previously offline everywhere. Best-effort: Redis failure
// is logged and swallowed so a degraded Redis can never take the gateway down.
// It reports whether the user is online after this connect.
func (s *Service) Connect(ctx context.Context, userID int64, connID string) bool {
	ch, err := s.store.Connect(ctx, userID, connID, s.cfg.Instance)
	if err != nil {
		s.log.Error("presence: connect", "user", userID, "conn", connID, "error", err)
		return false
	}
	if ch == ChangeOnline {
		s.broadcast(ctx, userID, "online", nil)
	}
	return true
}

// Disconnect removes a live connection and broadcasts the offline transition
// with the authoritative last-seen when the user has no live connections left
// anywhere. A stale removal (already cleaned up) never flips a user offline —
// the store guards this. It reports whether the user is offline now.
func (s *Service) Disconnect(ctx context.Context, userID int64, connID string) bool {
	ch, err := s.store.Disconnect(ctx, userID, connID, s.cfg.Instance)
	if err != nil {
		s.log.Error("presence: disconnect", "user", userID, "conn", connID, "error", err)
		return false
	}
	if ch == ChangeOffline {
		lastSeen := s.now().UTC()
		if err := s.store.SetLastSeen(ctx, userID, lastSeen); err != nil {
			s.log.Error("presence: last_seen", "user", userID, "error", err)
		}
		s.broadcast(ctx, userID, "offline", &lastSeen)
		return true
	}
	return false
}

// Touch refreshes the instance's presence liveness for a user with live local
// connections. Driven by the connection heartbeat sweeper (Sweeper), so a
// silent crash expires the user's presence within TTL.
func (s *Service) Touch(ctx context.Context, userID int64) {
	if err := s.store.Touch(ctx, userID, s.cfg.Instance); err != nil {
		// Transient Redis blip: the sweeper will retry next tick. Not fatal.
		s.log.Debug("presence: touch", "user", userID, "error", err)
	}
}

// Update applies a client presence.update (API.md §17.11): status +
// custom_status are stored as user-level meta and broadcast to the user's
// conversations. Best-effort and throttled per connection (1/s, §16.8).
func (s *Service) Update(ctx context.Context, userID int64, status, customStatus string) {
	if status == "" {
		status = "online"
	}
	if err := s.store.SetMeta(ctx, userID, status, customStatus); err != nil {
		s.log.Error("presence: update", "user", userID, "error", err)
		return
	}
	s.broadcast(ctx, userID, status, nil)
}

// SetConversation / DropConversation maintain the user's conversation-interest
// set (called on subscribe/unsubscribe). Best-effort; fan-out degrades to the
// user's own devices when the set is missing.
func (s *Service) SetConversation(ctx context.Context, userID int64, convID int64) {
	if err := s.store.ConvsAdd(ctx, userID, convID); err != nil {
		s.log.Debug("presence: convs add", "user", userID, "conv", convID, "error", err)
	}
}

func (s *Service) DropConversation(ctx context.Context, userID int64, convID int64) {
	if err := s.store.ConvsRemove(ctx, userID, convID); err != nil {
		s.log.Debug("presence: convs remove", "user", userID, "conv", convID, "error", err)
	}
}

// IsOnline aggregates presence across every gateway instance.
func (s *Service) IsOnline(ctx context.Context, userID int64) bool {
	online, err := s.store.IsOnline(ctx, userID)
	if err != nil {
		return false
	}
	return online
}

// LastSeen returns the authoritative server-side last-seen (empty when the
// user is online or none recorded).
func (s *Service) LastSeen(ctx context.Context, userID int64) (time.Time, error) {
	return s.store.GetLastSeen(ctx, userID)
}

func (s *Service) broadcast(ctx context.Context, userID int64, status string, lastSeen *time.Time) {
	if s.notifier == nil {
		return
	}
	convs, err := s.store.Convs(ctx, userID)
	if err != nil {
		s.log.Debug("presence: convs for broadcast", "user", userID, "error", err)
	}
	status, customStatus := s.resolveStatus(ctx, userID, status)
	s.notifier.NotifyPresence(ctx, ChangeEvent{
		UserID:          userID,
		Status:          status,
		CustomStatus:    customStatus,
		LastSeenAt:      lastSeen,
		ConversationIDs: convs,
	})
}

// resolveStatus layers the stored custom_status onto a status transition so a
// connect preserves a user's custom presence where the store has one.
func (s *Service) resolveStatus(ctx context.Context, userID int64, status string) (string, string) {
	_, custom, err := s.store.GetMeta(ctx, userID)
	if err != nil {
		return status, ""
	}
	return status, custom
}

// Sweeper periodically extends the presence TTL for every user with live local
// connections (heartbeat-based presence expiration). A connection whose socket
// is alive stays online because its user is touched here; a crashed instance
// stops touching and the TTL expires the user's presence. Run it as a
// goroutine; cancel ctx to stop.
type Sweeper struct {
	store  Store
	online OnlineUsers
	cfg    Config
	log    *slog.Logger
}

// OnlineUsers yields the user ids with at least one live local connection.
// *delivery/ws.Hub implements it.
type OnlineUsers interface {
	OnlineUsers() []int64
}

// NewSweeper builds the presence heartbeat sweeper.
func NewSweeper(store Store, online OnlineUsers, cfg Config, log *slog.Logger) *Sweeper {
	return &Sweeper{store: store, online: online, cfg: cfg, log: log}
}

// Run touches all online users every TTL/2 until ctx is cancelled. It blocks.
func (s *Sweeper) Run(ctx context.Context) error {
	if s.online == nil {
		return nil
	}
	interval := s.cfg.TTL / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.log.Info("presence: sweeper started", "interval", interval, "ttl", s.cfg.TTL)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for _, uid := range s.online.OnlineUsers() {
				if err := s.store.Touch(ctx, uid, s.cfg.Instance); err != nil {
					s.log.Debug("presence: sweep touch", "user", uid, "error", err)
				}
			}
		}
	}
}
