package ws

import (
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

func TestDispatchEphemeralSkipsReplay(t *testing.T) {
	hub, _ := newDispatcherTestHub(t)
	b := newReplayBuffer(DefaultReplayConfig())
	d := NewDispatcher(hub, b, observability.NewLogger("test"))

	conv := int64(2001)
	// Typing is transient: GlobalSeq 0, never persisted to the replay ring.
	d.dispatch(&domain.Event{
		GlobalSeq:      0,
		EventType:      domain.EventTypingIndicator,
		ConversationID: &conv,
		Payload:        []byte(`{"conversation_id":2001,"user_id":1001,"status":"typing"}`),
	})

	conn := hub.conns["c-d"]
	select {
	case raw := <-conn.send:
		f, err := domain.Decode(raw)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Type != domain.EventTypingIndicator {
			t.Errorf("type = %q, want typing.indicator", f.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ephemeral never delivered to subscriber")
	}

	if got := b.since(conv, 0); len(got) != 0 {
		t.Errorf("ephemeral leaked into replay buffer: %+v", got)
	}
}

func TestDispatchPresenceReachesOnlySubscribers(t *testing.T) {
	hub, _ := newDispatcherTestHub(t)
	d := NewDispatcher(hub, newReplayBuffer(DefaultReplayConfig()), observability.NewLogger("test"))

	// Same presence change published to two conversations; only the
	// subscribed one must fan out.
	for _, c := range []int64{2001, 2002} {
		d.dispatch(&domain.Event{
			GlobalSeq:      0,
			EventType:      domain.EventPresenceChanged,
			ConversationID: &c,
			Payload:        []byte(`{"user_id":1001,"presence":{"status":"busy"}}`),
		})
	}

	conn := hub.conns["c-d"]
	select {
	case raw := <-conn.send:
		f, _ := domain.Decode(raw)
		if f.Type != domain.EventPresenceChanged {
			t.Errorf("type = %q, want presence.changed", f.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("presence event never delivered")
	}

	// Unsubscribed conversation produced nothing (single frame observed).
	select {
	case extra := <-conn.send:
		t.Errorf("unexpected extra frame for unsubscribed conversation: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchDeduplicatesAtLeastOnce(t *testing.T) {
	hub, h := newDispatcherTestHub(t)
	d := NewDispatcher(hub, newReplayBuffer(DefaultReplayConfig()), observability.NewLogger("test"))

	conv := int64(2001)
	ev := &domain.Event{
		GlobalSeq:      42,
		EventType:      "message.created",
		ConversationID: &conv,
		Payload:        []byte(`{"id":7,"conversation_id":2001}`),
	}
	d.dispatch(ev)
	d.dispatch(ev) // relay retry / at-least-once duplicate

	conn := hub.conns["c-d"]
	select {
	case <-conn.send:
	case <-time.After(2 * time.Second):
		t.Fatal("deduped event never delivered")
	}
	// The duplicate must be collapsed: nothing second follows.
	select {
	case extra := <-conn.send:
		t.Errorf("duplicate global_seq dispatched again: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
	if h == nil {
		t.Fatal("unreachable")
	}
}

func TestDispatchDedupeWindowBounded(t *testing.T) {
	// No connections registered: nothing to fan out to, so this test purely
	// exercises dedupe-window eviction and the replay ring's bound without a
	// consumer (a 5000-event burst would legitimately trip slow-consumer drop).
	hub := NewHub(DefaultConfig(), newRecordingHandler(), observability.NewLogger("test"))
	b := newReplayBuffer(DefaultReplayConfig())
	d := NewDispatcher(hub, b, observability.NewLogger("test"))

	conv := int64(2001)
	// Larger than the dedupe window; the oldest entry is evicted so a stale
	// re-delivery would re-fire. Only assertion here: no panic, no lockup.
	for i := int64(1); i <= 5000; i++ {
		d.dispatch(&domain.Event{GlobalSeq: i, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{}`)})
	}
	d.dispatch(&domain.Event{GlobalSeq: 1, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{}`)})

	// Ring stays bounded at MaxEventsPerConversation.
	if got := b.since(conv, 0); len(got) > 1000 {
		t.Errorf("ring len = %d, want bounded", len(got))
	}
}
