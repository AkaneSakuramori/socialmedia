package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
)

// Publisher publishes one message to the Redis backplane. The infra relay's
// RedisPublisher implements it; the interface is redeclared here (like
// dispatcherChannel) so the delivery layer has no import cycle with infra.
type Publisher interface {
	Publish(ctx context.Context, channel string, msg []byte) error
}

// backplane returns the channel ephemeral events share with the outbox relay
// (dispatcherChannel, declared in dispatcher.go). Kept as a function so the
// notifiers read the same channel the dispatcher subscribes to.
func backplane() string { return dispatcherChannel }

// presenceNotifier fans presence transitions out via the backplane: one
// presence.changed event per conversation in the user's interest set. Every
// gateway instance's dispatcher then delivers to that conversation's local
// subscribers (including the user's own other devices — multi-device sync).
// Ephemeral: never replayed.
type presenceNotifier struct {
	pub Publisher
	log *slog.Logger
}

func newPresenceNotifier(pub Publisher, log *slog.Logger) *presenceNotifier {
	return &presenceNotifier{pub: pub, log: log}
}

// NewPresenceNotifier builds the backplane presence notifier (exported for the
// composition root).
func NewPresenceNotifier(pub Publisher, log *slog.Logger) *presenceNotifier {
	return newPresenceNotifier(pub, log)
}

// NotifyPresence publishes presence.changed (API.md §18.12) for each
// conversation in the user's interest set. Best-effort: a publish failure is
// logged, never fatal — presence is ephemeral and converges on the next
// transition.
func (n *presenceNotifier) NotifyPresence(ctx context.Context, ev presence.ChangeEvent) {
	payload, err := json.Marshal(map[string]any{
		"user_id":      strconv.FormatInt(ev.UserID, 10),
		"presence":     map[string]any{"status": ev.Status, "custom_status": ev.CustomStatus},
		"last_seen_at": lastSeenWire(ev.LastSeenAt),
	})
	if err != nil {
		n.log.Error("realtime: marshal presence.changed", "user", ev.UserID, "error", err)
		return
	}
	actor := ev.UserID
	for _, convID := range ev.ConversationIDs {
		e := domain.Event{
			GlobalSeq:      0, // ephemeral — never deduped/replayed by seq
			EventType:      domain.EventPresenceChanged,
			ConversationID: &convID,
			ActorUserID:    &actor,
			Payload:        payload,
		}
		b, err := e.Encode()
		if err != nil {
			n.log.Error("realtime: encode presence.changed", "user", ev.UserID, "error", err)
			continue
		}
		if err := n.pub.Publish(ctx, backplane(), b); err != nil {
			n.log.Debug("realtime: publish presence.changed", "user", ev.UserID, "conv", convID, "error", err)
		}
	}
}

// lastSeenWire renders an optional last-seen as an RFC3339 string or null
// (API.md §18.12).
func lastSeenWire(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

// typingNotifier fans typing indicators out via the backplane: a
// typing.indicator event per conversation. Every gateway instance's dispatcher
// delivers to the conversation's subscribers. Ephemeral: never replayed.
type typingNotifier struct {
	pub Publisher
	log *slog.Logger
}

func newTypingNotifier(pub Publisher, log *slog.Logger) *typingNotifier {
	return &typingNotifier{pub: pub, log: log}
}

// NewTypingNotifier builds the backplane typing notifier (exported for the
// composition root).
func NewTypingNotifier(pub Publisher, log *slog.Logger) *typingNotifier {
	return newTypingNotifier(pub, log)
}

// NotifyTyping publishes typing.indicator (API.md §18.11). Best-effort: a
// publish failure is logged, never fatal.
func (n *typingNotifier) NotifyTyping(ctx context.Context, ev typing.ChangeEvent) {
	payload, err := json.Marshal(map[string]any{
		"conversation_id": strconv.FormatInt(ev.ConversationID, 10),
		"user_id":         strconv.FormatInt(ev.UserID, 10),
		"status":          ev.Status,
	})
	if err != nil {
		n.log.Error("realtime: marshal typing.indicator", "user", ev.UserID, "conv", ev.ConversationID, "error", err)
		return
	}
	conv := ev.ConversationID
	actor := ev.UserID
	e := domain.Event{
		GlobalSeq:      0, // ephemeral
		EventType:      domain.EventTypingIndicator,
		ConversationID: &conv,
		ActorUserID:    &actor,
		Payload:        payload,
	}
	b, err := e.Encode()
	if err != nil {
		n.log.Error("realtime: encode typing.indicator", "user", ev.UserID, "error", err)
		return
	}
	if err := n.pub.Publish(ctx, backplane(), b); err != nil {
		n.log.Debug("realtime: publish typing.indicator", "user", ev.UserID, "conv", conv, "error", err)
	}
}
