package domain

import (
	"testing"
)

func TestEventEncodeDecodeRoundTrip(t *testing.T) {
	conv := int64(5)
	actor := int64(7)
	e := &Event{
		GlobalSeq:       41,
		EventType:       "message.created",
		ConversationID:  &conv,
		EntityID:        &actor,
		ActorUserID:     &actor,
		AffectedUserIDs: []int64{7, 9},
		Payload:         []byte(`{"text":"hi"}`),
	}
	b, err := e.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeEvent(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.GlobalSeq != e.GlobalSeq || got.EventType != e.EventType {
		t.Errorf("global_seq/type = %d/%q, want %d/%q", got.GlobalSeq, got.EventType, e.GlobalSeq, e.EventType)
	}
	if got.ConversationID == nil || *got.ConversationID != conv {
		t.Errorf("conversation_id = %v, want %d", got.ConversationID, conv)
	}
	if got.EntityID == nil || *got.EntityID != actor {
		t.Errorf("entity_id = %v, want %d", got.EntityID, actor)
	}
	if len(got.AffectedUserIDs) != 2 || got.AffectedUserIDs[1] != 9 {
		t.Errorf("affected_user_ids = %v", got.AffectedUserIDs)
	}
	if string(got.Payload) != `{"text":"hi"}` {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestDecodeEventRejectsMissingType(t *testing.T) {
	if _, err := DecodeEvent([]byte(`{"global_seq":1}`)); err == nil {
		t.Fatal("want error for missing event_type")
	}
}

func TestEventTypeToWire(t *testing.T) {
	cases := map[string]string{
		"message.created":         EventMessageCreated,
		"message.edited":          EventMessageEdited,
		"message.deleted":         EventMessageDeleted,
		"receipt.read":            EventServerReceiptRead,
		"receipt.delivered":       EventServerReceiptDelivered,
		"conversation.created":    EventConvCreated,
		"conversation.membership": EventMembership,
		"conversation.settings":   EventConvUpdated,
		"media.ready":             EventMediaReady,
	}
	for from, want := range cases {
		got, ok := EventTypeToWire(from)
		if !ok {
			t.Errorf("EventTypeToWire(%q) not ok", from)
			continue
		}
		if got != want {
			t.Errorf("EventTypeToWire(%q) = %q, want %q", from, got, want)
		}
	}
	if _, ok := EventTypeToWire("message.reaction"); ok {
		t.Error("message.reaction must be resolved from payload, not a stable mapping")
	}
	if _, ok := EventTypeToWire("bogus"); ok {
		t.Error("unknown event type must not map")
	}
}
