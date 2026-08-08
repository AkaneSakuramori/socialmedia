package typing

import (
	"context"
	"log/slog"
	"time"
)

// ChangeEvent is a typing indicator transition reported to the notifier
// (API.md §18.11): status is "typing" or "stopped".
type ChangeEvent struct {
	UserID         int64
	ConversationID int64
	Status         string
	At             time.Time
}

// Notifier receives typing indicator transitions. The delivery layer
// implements it by publishing typing.indicator events onto the Redis backplane
// so every gateway instance fans them out to the conversation's subscribers.
type Notifier interface {
	NotifyTyping(ctx context.Context, ev ChangeEvent)
}

// Service orchestrates typing state, throttling, expiry, and fan-out. It is
// the only way the realtime layer touches typing state; the store is injected
// so unit tests run without Redis.
type Service struct {
	store    Store
	cfg      Config
	log      *slog.Logger
	notifier Notifier // optional (nil → no fan-out)
	now      func() time.Time
}

// NewService builds the typing service. notifier may be nil (state still
// tracked, just not broadcast).
func NewService(store Store, cfg Config, notifier Notifier, log *slog.Logger) *Service {
	return &Service{store: store, cfg: cfg, log: log, notifier: notifier, now: time.Now}
}

// Start handles a typing.start frame (API.md §17.10). The state is stored and
// a typing.indicator is broadcast only when the (user, conversation) throttle
// allows (≥2s since the last broadcast for this sender). Best-effort: a Redis
// failure degrades to no indicator, never an error to the client.
func (s *Service) Start(ctx context.Context, userID, convID int64) {
	allowed, err := s.store.Broadcast(ctx, userID, convID)
	if err != nil {
		s.log.Error("typing: start", "user", userID, "conv", convID, "error", err)
		return
	}
	if !allowed {
		s.log.Debug("typing: throttled", "user", userID, "conv", convID)
		return
	}
	s.emit(ctx, userID, convID, "typing")
}

// Stop handles a typing.stop frame: clears the state and broadcasts "stopped"
// (never throttled — a stop must always land so the indicator is hidden).
func (s *Service) Stop(ctx context.Context, userID, convID int64) {
	existed, err := s.store.Stop(ctx, userID, convID)
	if err != nil {
		s.log.Error("typing: stop", "user", userID, "conv", convID, "error", err)
		return
	}
	if existed {
		s.emit(ctx, userID, convID, "stopped")
	}
}

// CleanupUser clears all of a user's typing state (connection teardown) and
// broadcasts "stopped" to each affected conversation so typing can never get
// stuck on for a disconnected user (ARCHITECTURE.md §16.1).
func (s *Service) CleanupUser(ctx context.Context, userID int64) {
	convs, err := s.store.CleanupUser(ctx, userID)
	if err != nil {
		s.log.Error("typing: cleanup", "user", userID, "error", err)
		return
	}
	for _, convID := range convs {
		s.emit(ctx, userID, convID, "stopped")
	}
}

// IsTyping reports whether a user currently has typing state in a conversation.
func (s *Service) IsTyping(ctx context.Context, userID, convID int64) bool {
	ok, err := s.store.IsTyping(ctx, userID, convID)
	if err != nil {
		return false
	}
	return ok
}

func (s *Service) emit(ctx context.Context, userID, convID int64, status string) {
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyTyping(ctx, ChangeEvent{
		UserID:         userID,
		ConversationID: convID,
		Status:         status,
		At:             s.now().UTC(),
	})
}
