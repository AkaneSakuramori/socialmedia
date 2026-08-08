package ws

import (
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// newDispatcherTestHub builds a hub with a recording handler and one registered
// connection for user 1001 subscribed to conversation 2001.
func newDispatcherTestHub(t *testing.T) (*Hub, *recordingHandler) {
	t.Helper()
	h := newRecordingHandler()
	cfg := DefaultConfig()
	cfg.PingInterval = time.Hour // no heartbeat interference
	hub := NewHub(cfg, h, observability.NewLogger("test"))
	c := newRegisteredConn(hub, "c-d", 1001, 7001)
	hub.Subscribe(c, 2001)
	t.Cleanup(func() { hub.unregister(c) })
	return hub, h
}

func TestDispatchConversationEventReachesSubscribers(t *testing.T) {
	hub, h := newDispatcherTestHub(t)
	d := NewDispatcher(hub, newReplayBuffer(DefaultReplayConfig()), observability.NewLogger("test"))

	conv := int64(2001)
	d.dispatch(&domain.Event{
		GlobalSeq:      5,
		EventType:      "message.created",
		ConversationID: &conv,
		Payload:        []byte(`{"id":1,"conversation_id":2001,"sequence":1}`),
	})

	select {
	case f := <-h.got:
		t.Fatalf("recording handler must not receive fan-out frames (got %s)", f.Type)
	case <-time.After(50 * time.Millisecond):
	}

	// The registered connection's send buffer must carry the event.
	conn := hub.conns["c-d"]
	select {
	case b := <-conn.send:
		f, err := domain.Decode(b)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Type != domain.EventMessageCreated {
			t.Errorf("type = %q, want message.created", f.Type)
		}
		if f.Seq == nil || *f.Seq != 1 {
			t.Errorf("seq = %v, want 1 (first S2C frame)", f.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fan-out event never enqueued")
	}
}

func TestDispatchReactionSplitsByPayload(t *testing.T) {
	hub, _ := newDispatcherTestHub(t)
	d := NewDispatcher(hub, newReplayBuffer(DefaultReplayConfig()), observability.NewLogger("test"))

	conv := int64(2001)
	d.dispatch(&domain.Event{
		GlobalSeq:      6,
		EventType:      domain.EventMessageReaction,
		ConversationID: &conv,
		Payload:        []byte(`{"added":true,"message_id":1}`),
	})
	d.dispatch(&domain.Event{
		GlobalSeq:      7,
		EventType:      domain.EventMessageReaction,
		ConversationID: &conv,
		Payload:        []byte(`{"added":false,"message_id":1}`),
	})

	conn := hub.conns["c-d"]
	first := <-conn.send
	second := <-conn.send
	f1, _ := domain.Decode(first)
	f2, _ := domain.Decode(second)
	if f1.Type != domain.EventReactionAdded {
		t.Errorf("first type = %q, want reaction.added", f1.Type)
	}
	if f2.Type != domain.EventReactionRemoved {
		t.Errorf("second type = %q, want reaction.removed", f2.Type)
	}
}

func TestDispatchReplayBufferReplaysGap(t *testing.T) {
	hub, _ := newDispatcherTestHub(t)
	d := NewDispatcher(hub, newReplayBuffer(DefaultReplayConfig()), observability.NewLogger("test"))

	conv := int64(2001)
	for _, e := range []*domain.Event{
		{GlobalSeq: 10, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{"id":1}`)},
		{GlobalSeq: 11, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{"id":2}`)},
		{GlobalSeq: 12, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{"id":3}`)},
	} {
		d.dispatch(e)
	}

	// Client last processed global_seq 10: replay 11 and 12 in order.
	got := d.replay.since(conv, 10)
	if len(got) != 2 {
		t.Fatalf("replay len = %d, want 2", len(got))
	}
	if got[0].seq != 11 || got[1].seq != 12 {
		t.Errorf("replay seqs = %d,%d want 11,12", got[0].seq, got[1].seq)
	}
	if got[0].wire != domain.EventMessageCreated {
		t.Errorf("replay wire = %q", got[0].wire)
	}
	if d.replay.since(conv, 12) != nil && len(d.replay.since(conv, 12)) != 0 {
		t.Error("replay must be empty past the last global_seq")
	}
}

func TestReplayBufferCapsRing(t *testing.T) {
	b := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 3, TTL: time.Hour})
	conv := int64(9)
	for i := int64(1); i <= 5; i++ {
		b.append(&domain.Event{GlobalSeq: i, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{}`)})
	}
	got := b.since(conv, 0)
	if len(got) != 3 {
		t.Fatalf("ring len = %d, want 3 (bounded)", len(got))
	}
	if got[0].seq != 3 {
		t.Errorf("oldest kept seq = %d, want 3", got[0].seq)
	}
}

func TestReplayBufferPrunesExpired(t *testing.T) {
	b := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 10, TTL: time.Hour})
	conv := int64(9)
	b.append(&domain.Event{GlobalSeq: 1, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{}`)})
	b.events[conv][0].at = time.Now().Add(-2 * time.Hour) // simulate expiry
	b.append(&domain.Event{GlobalSeq: 2, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{}`)})

	got := b.since(conv, 0)
	if len(got) != 1 || got[0].seq != 2 {
		t.Fatalf("after prune got %+v, want only seq 2", got)
	}
}

func TestEventWireTypeUnknownDrops(t *testing.T) {
	if got := eventWireType(&domain.Event{EventType: "bogus"}); got != "" {
		t.Errorf("wire = %q, want empty", got)
	}
}
