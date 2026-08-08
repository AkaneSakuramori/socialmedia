// Package infra holds the realtime module's infrastructure adapters: the
// outbox relay that moves committed change_log rows onto the Redis pub/sub
// backplane (ARCHITECTURE.md §13.1, ENGINEERING.md §18.2).
package infra

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	rt "github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// relayChannel is the single Redis pub/sub channel carrying committed realtime
// events. Every gateway instance's dispatcher subscribes here and filters with
// its local connection registry (ENGINEERING.md §18.3); per-conversation
// channels (conv:{id}:events, ARCHITECTURE.md §13.4) are a scale-out shard
// that can be layered on without protocol change.
const relayChannel = "realtime:events"

// RelayConfig tunes the outbox relay's polling loop.
type RelayConfig struct {
	// PollInterval is how often the relay checks for new change_log rows.
	PollInterval time.Duration
	// BatchSize is the max rows fetched per poll (ListAfter limit).
	BatchSize int64
}

// DefaultRelayConfig returns the production-default relay settings.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{PollInterval: 100 * time.Millisecond, BatchSize: 500}
}

// OutboxStore is the minimal surface the relay needs over change_log.
type OutboxStore interface {
	// Head returns the highest committed global_seq (0 when empty).
	Head(ctx context.Context) (int64, error)
	// ListAfter returns committed rows with global_seq > after in global order.
	ListAfter(ctx context.Context, after, limit int64) ([]domain.ChangeLogRow, error)
}

// Publisher publishes one realtime event to the backplane.
type Publisher interface {
	Publish(ctx context.Context, channel string, msg []byte) error
}

// RedisPublisher adapts *redis.Client to Publisher.
type RedisPublisher struct{ client *redis.Client }

// NewRedisPublisher wraps the shared redis client.
func NewRedisPublisher(client *redis.Client) *RedisPublisher {
	return &RedisPublisher{client: client}
}

// Publish pushes a message onto the channel (go-redis Publish is async-safe).
func (p *RedisPublisher) Publish(ctx context.Context, channel string, msg []byte) error {
	return p.client.Publish(ctx, channel, msg).Err()
}

// Relay polls the transactional outbox (change_log) and publishes each
// committed row to the Redis pub/sub backplane (ARCHITECTURE.md §13.1, §37.4
// outbox pattern). It is the durable path behind realtime fan-out and sync:
// rows commit atomically with their business data, so an event never appears
// without its data (DATABASE.md §7.1). Delivery is at-least-once; consumers
// dedupe by global_seq.
type Relay struct {
	store OutboxStore
	pub   Publisher
	cfg   RelayConfig
	log   *slog.Logger
	// cursor is the last global_seq published. It starts at the current head:
	// pre-existing history is not re-published (realtime only needs live
	// events; sync backfills the rest).
	cursor int64
}

// NewRelay builds the outbox relay.
func NewRelay(store OutboxStore, pub Publisher, cfg RelayConfig, log *slog.Logger) *Relay {
	return &Relay{store: store, pub: pub, cfg: cfg, log: log}
}

// Run polls change_log until ctx is cancelled. It blocks; run it as a
// goroutine and cancel ctx to stop.
func (r *Relay) Run(ctx context.Context) error {
	head, err := r.store.Head(ctx)
	if err != nil {
		return fmt.Errorf("realtime: relay initial head: %w", err)
	}
	r.cursor = head
	r.log.Info("realtime: outbox relay started", "from_global_seq", head, "channel", relayChannel)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	pump := func() error {
		rows, err := r.store.ListAfter(ctx, r.cursor, r.cfg.BatchSize)
		if err != nil {
			return err
		}
		for _, row := range rows {
			e := rt.Event{
				GlobalSeq:       row.GlobalSeq,
				EventType:       row.EventType,
				ConversationID:  row.ConversationID,
				EntityID:        row.EntityID,
				ActorUserID:     row.ActorUserID,
				AffectedUserIDs: row.AffectedUserIDs,
				Payload:         row.Payload,
			}
			b, err := e.Encode()
			if err != nil {
				return fmt.Errorf("realtime: relay encode event %d: %w", e.GlobalSeq, err)
			}
			if err := r.pub.Publish(ctx, relayChannel, b); err != nil {
				return fmt.Errorf("realtime: relay publish %d: %w", e.GlobalSeq, err)
			}
			r.cursor = e.GlobalSeq
		}
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			r.log.Info("realtime: outbox relay stopped", "at_global_seq", r.cursor)
			return nil
		case <-ticker.C:
			if err := pump(); err != nil {
				// Backpressure-safe: log and keep polling. A transient Redis or
				// PG error must not kill the relay; the next poll re-reads from
				// the last published cursor (at-least-once).
				r.log.Error("realtime: relay poll failed", "error", err, "cursor", r.cursor)
			}
		}
	}
}

// Channel returns the backplane channel the relay publishes to.
func Channel() string { return relayChannel }
