package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// dispatcherChannel is the channel the relay publishes committed events to
// (must match infra.Channel). It is redeclared here so the delivery layer has
// no import cycle with infra; keep the two in sync. Ephemeral presence/typing
// events are published to the same channel.
const dispatcherChannel = "realtime:events"

// Dispatcher consumes the log-dispatch event stream (ARCHITECTURE.md §13.1)
// and fans each event out through the local Hub. It is the "log → dispatch"
// half of realtime delivery: the outbox relay publishes committed change_log
// rows; every gateway instance subscribes here and delivers to its own local
// connections only (ENGINEERING.md §18.2/§18.3). Events carry a precomputed
// fan-out target (affected_user_ids, DATABASE.md §7.1); the dispatcher routes
// conversation-scoped events to subscribers and user-scoped events to the
// affected users' sockets. Delivery is at-least-once; a bounded dedupe set
// collapses redundant global_seqs.
type Dispatcher struct {
	hub *Hub
	// replay is the per-conversation replay buffer for resume (API.md §16.6,
	// ENGINEERING.md §18.3). Bounded and TTL'd; see replayBuffer. It is shared
	// with the frame handler so resume can replay the gap.
	replay *replayBuffer
	// dedupe collapses at-least-once duplicates by global_seq (bounded).
	dedupe *dedupeSet
	log    *slog.Logger
}

// NewDispatcher builds the dispatcher over a hub, sharing the given replay
// buffer with the frame handler (the handler reads it on resume).
func NewDispatcher(hub *Hub, replay *replayBuffer, log *slog.Logger) *Dispatcher {
	return &Dispatcher{hub: hub, replay: replay, dedupe: newDedupeSet(4096), log: log}
}

// Run subscribes to the event stream and dispatches until ctx is cancelled.
// It blocks; run it as a goroutine and cancel ctx to stop.
func (d *Dispatcher) Run(ctx context.Context, client *redis.Client) error {
	sub := client.Subscribe(ctx, dispatcherChannel)
	ch := sub.Channel()
	defer func() { _ = sub.Close() }()

	d.log.Info("realtime: dispatcher subscribed", "channel", dispatcherChannel)
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return fmt.Errorf("realtime: dispatcher channel closed")
			}
			e, err := domain.DecodeEvent([]byte(msg.Payload))
			if err != nil {
				d.log.Warn("realtime: dispatcher undecodable event", "error", err)
				continue
			}
			d.dispatch(e)
		}
	}
}

// dispatch routes one event to the hub. Conversation-scoped events are fanned
// out to the conversation's subscribers; user-scoped events (and conversation
// events) go to the affected users' sockets. Durable events are appended to
// the replay buffer so a reconnecting client can resume the gap; ephemeral
// presence/typing events never enter the replay buffer.
func (d *Dispatcher) dispatch(e *domain.Event) {
	// At-least-once dedupe by global_seq (relay restarts / retries).
	if e.GlobalSeq > 0 && !d.dedupe.add(e.GlobalSeq) {
		d.log.Debug("realtime: dispatcher duplicate dropped", "global_seq", e.GlobalSeq)
		return
	}

	// Ephemeral events are routed to conversation subscribers and never
	// replayed (ARCHITECTURE.md §16/§15: typing + presence are transient).
	if domain.IsEphemeral(e.EventType) {
		if e.ConversationID != nil {
			d.hub.DeliverToConversation(*e.ConversationID, e.EventType, jsonPayload(e.Payload))
		}
		return
	}

	wireType := eventWireType(e)
	if wireType == "" {
		d.log.Warn("realtime: dispatcher unknown event type", "event_type", e.EventType, "global_seq", e.GlobalSeq)
		return
	}

	payload := e.Payload
	d.replay.append(e)

	// Conversation-scoped events reach the conversation's subscribers directly
	// (the sender's own sockets are included — they are subscribed too).
	if e.ConversationID != nil {
		d.hub.DeliverToConversation(*e.ConversationID, wireType, jsonPayload(payload))
	}

	// Events whose fan-out target is users, not the conversation subscriber
	// set, are delivered per affected user (receipts, membership, settings).
	switch e.EventType {
	case domain.ChangeLogConversationCreated, domain.ChangeLogConversationMembership,
		domain.ChangeLogConversationSettings, domain.ChangeLogReceiptRead,
		domain.ChangeLogReceiptDelivered, domain.EventUserUpdated:
		for _, uid := range e.AffectedUserIDs {
			d.hub.DeliverToUser(uid, wireType, jsonPayload(payload))
		}
	}
}

// eventWireType resolves the S2C frame type for a committed event. message.
// reaction splits by the payload's "added" flag (API.md §18.8); everything
// else is the stable change_log → wire mapping.
func eventWireType(e *domain.Event) string {
	if e.EventType == domain.EventMessageReaction {
		var p struct {
			Added bool `json:"added"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return ""
		}
		if p.Added {
			return domain.EventReactionAdded
		}
		return domain.EventReactionRemoved
	}
	t, _ := domain.EventTypeToWire(e.EventType)
	return t
}

// jsonPayload wraps raw JSON as json.RawMessage so sendEvent marshals it
// without double-encoding.
func jsonPayload(b []byte) json.RawMessage { return json.RawMessage(b) }

// --- replay buffer (resume support, API.md §16.6) ---

// ReplayConfig sizes the per-conversation replay buffer.
type ReplayConfig struct {
	// MaxEventsPerConversation bounds the buffer per conversation (bounded by
	// (max frame rate) × (buffer TTL), ENGINEERING.md §18.3).
	MaxEventsPerConversation int
	// TTL is how long an event stays replayable (30s default).
	TTL time.Duration
}

// DefaultReplayConfig returns the production-default replay buffer settings.
func DefaultReplayConfig() ReplayConfig {
	return ReplayConfig{MaxEventsPerConversation: 500, TTL: 30 * time.Second}
}

// replayBuffer is a bounded, TTL'd ring of recent events per conversation,
// keyed by global_seq. It backs the resume protocol's replay of the gap since
// the client's last_global_seq (API.md §16.6). A connection's per-connection
// seq is stamped at delivery time by sendEvent; replay stamps fresh seqs so
// the client sees a contiguous sequence after reconnection. It is safe for
// concurrent use: the dispatcher appends from its subscription goroutine while
// the frame handler replays from a connection's read pump.
type replayBuffer struct {
	mu  sync.Mutex
	cfg ReplayConfig
	// events[convID] is a FIFO ring of (global_seq, wire type, payload).
	events map[int64][]replayEvent
}

type replayEvent struct {
	seq     int64
	wire    string
	payload []byte
	at      time.Time
}

// ReplayFrame is one event delivered to a resuming connection (fresh
// per-connection seq, wire type, payload, original timestamp).
type ReplayFrame struct {
	Seq  int64
	Type string
	At   time.Time
	Data json.RawMessage
}

// Replayer is the resume-time view of the replay buffer. The frame handler
// consumes it; *replayBuffer implements it.
type Replayer interface {
	// CanReplay reports whether the buffer can replay the whole gap after
	// lastGlobalSeq — i.e. no per-conversation ring evicted events the client
	// is missing (buffer TTL exceeded or ring overflow).
	CanReplay(lastGlobalSeq int64) bool
	// ReplaySince returns the events with global_seq > lastGlobalSeq across
	// every conversation, ordered by global_seq (resume replay, API.md §16.6).
	ReplaySince(lastGlobalSeq int64) []ReplayFrame
}

func newReplayBuffer(cfg ReplayConfig) *replayBuffer {
	return &replayBuffer{cfg: cfg, events: make(map[int64][]replayEvent)}
}

// NewReplayBuffer builds the shared resume replay buffer (exported for the
// composition root, which shares one buffer between the dispatcher and the
// frame handler).
func NewReplayBuffer(cfg ReplayConfig) *replayBuffer {
	return newReplayBuffer(cfg)
}

// append records an event for later replay (best-effort; expired entries are
// pruned opportunistically).
func (b *replayBuffer) append(e *domain.Event) {
	if e.ConversationID == nil {
		return
	}
	conv := *e.ConversationID
	wire := eventWireType(e)
	if wire == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ring := b.events[conv]
	now := time.Now()
	// Prune expired entries and cap the ring.
	kept := ring[:0]
	for _, ev := range ring {
		if now.Sub(ev.at) <= b.cfg.TTL {
			kept = append(kept, ev)
		}
	}
	if len(kept) >= b.cfg.MaxEventsPerConversation {
		kept = kept[len(kept)-b.cfg.MaxEventsPerConversation+1:]
	}
	b.events[conv] = append(kept, replayEvent{seq: e.GlobalSeq, wire: wire, payload: e.Payload, at: now})
}

// since returns the replayable events with global_seq > last, in order.
func (b *replayBuffer) since(conv int64, last int64) []replayEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]replayEvent, 0)
	for _, ev := range b.events[conv] {
		if ev.seq > last {
			out = append(out, ev)
		}
	}
	return out
}

// CanReplay reports whether every non-empty per-conversation ring can cover
// the client's cursor. If any ring's oldest retained global_seq is newer than
// last+1, events between last and the ring floor were evicted and the gap
// cannot be replayed (API.md §16.6 → resume_rejected, buffer_expired).
func (b *replayBuffer) CanReplay(lastGlobalSeq int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) == 0 {
		return false
	}
	for _, ring := range b.events {
		if len(ring) == 0 {
			continue
		}
		if lastGlobalSeq+1 < ring[0].seq {
			return false
		}
	}
	return true
}

// ReplaySince collects every buffered event with global_seq > lastGlobalSeq
// across all conversations, ordered by global_seq. The result is bounded by
// the buffer's per-conversation caps.
func (b *replayBuffer) ReplaySince(lastGlobalSeq int64) []ReplayFrame {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []ReplayFrame
	for _, ring := range b.events {
		for _, ev := range ring {
			if ev.seq > lastGlobalSeq {
				out = append(out, ReplayFrame{Seq: ev.seq, Type: ev.wire, At: ev.at, Data: json.RawMessage(ev.payload)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// dedupeSet collapses at-least-once duplicates by global_seq. It is bounded
// and FIFO: once full, the oldest seen seq is forgotten. Ephemeral events
// (global_seq 0) are never deduped.
type dedupeSet struct {
	mu    sync.Mutex
	seen  map[int64]struct{}
	order []int64
	max   int
}

func newDedupeSet(max int) *dedupeSet {
	return &dedupeSet{seen: make(map[int64]struct{}), max: max}
}

// add records seq and reports whether it is new (true = first sighting).
func (d *dedupeSet) add(seq int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[seq]; ok {
		return false
	}
	d.seen[seq] = struct{}{}
	d.order = append(d.order, seq)
	if len(d.order) > d.max {
		old := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, old)
	}
	return true
}
