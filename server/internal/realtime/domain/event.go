// Package domain defines the realtime protocol contract (API.md §16–§18):
// the frame envelope, event vocabulary, close codes, and the log-dispatch
// event that the outbox relay publishes and the dispatcher routes.
package domain

import (
	"encoding/json"
	"fmt"
)

// Event is the log-dispatch envelope carried between the outbox relay and the
// dispatcher (ARCHITECTURE.md §13.1). It is a change_log row in wire form:
// self-contained, ordered by GlobalSeq, with the fan-out target precomputed in
// AffectedUserIDs (DATABASE.md §7.1). The relay publishes one Event per
// committed change_log row; the dispatcher decodes it and routes it through the
// local Hub (delivery/ws). Consumers dedupe by GlobalSeq — delivery is
// at-least-once (ENGINEERING.md §23).
type Event struct {
	GlobalSeq       int64   `json:"global_seq"`
	EventType       string  `json:"event_type"`
	ConversationID  *int64  `json:"conversation_id,omitempty"`
	EntityID        *int64  `json:"entity_id,omitempty"`
	ActorUserID     *int64  `json:"actor_user_id,omitempty"`
	AffectedUserIDs []int64 `json:"affected_user_ids,omitempty"`
	Payload         []byte  `json:"payload"`
}

// Encode serializes the event for the Redis pub/sub backplane.
func (e *Event) Encode() ([]byte, error) {
	return json.Marshal(e)
}

// DecodeEvent parses a pub/sub payload into an Event.
func DecodeEvent(b []byte) (*Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("realtime: decode event: %w", err)
	}
	if e.EventType == "" {
		return nil, fmt.Errorf("realtime: event missing event_type")
	}
	return &e, nil
}

// EventTypeToWire maps a change_log event_type to the server→client frame type
// (API.md §18). Events without a 1:1 wire name (e.g. message.reaction, which
// splits into reaction.added/removed) are resolved by the dispatcher from the
// payload; EventTypeToWire covers the stable mappings.

// EventMessageReaction is the change_log event_type for reaction adds/removes
// (the wire type is reaction.added or reaction.removed depending on payload).
const EventMessageReaction = "message.reaction"

// EventUserUpdated is the change_log event_type for profile/settings updates
// (wire type settings.updated, API.md §18.20).
const EventUserUpdated = "user.updated"

// Ephemeral event types carried on the backplane. Unlike change_log rows they
// are never persisted and never replayed on resume (ARCHITECTURE.md §16:
// typing is transient by design; presence is TTL'd). The dispatcher routes
// them to conversation subscribers and skips the replay buffer. The wire
// constants are declared in protocol.go (EventPresenceChanged, §18.12;
// EventTypingIndicator, §18.11).

// IsEphemeral reports whether an event type is transient realtime state
// (never replayed, never part of change_log).
func IsEphemeral(eventType string) bool {
	return eventType == EventPresenceChanged || eventType == EventTypingIndicator
}

// Change-log event types used by the dispatcher's routing decision (the wire
// fan-out targets are user sockets, not the conversation subscriber set).
const (
	ChangeLogConversationCreated    = "conversation.created"
	ChangeLogConversationMembership = "conversation.membership"
	ChangeLogConversationSettings   = "conversation.settings"
	ChangeLogReceiptRead            = "receipt.read"
	ChangeLogReceiptDelivered       = "receipt.delivered"
)

func EventTypeToWire(eventType string) (string, bool) {
	switch eventType {
	case "message.created":
		return EventMessageCreated, true
	case "message.edited":
		return EventMessageEdited, true
	case "message.deleted":
		return EventMessageDeleted, true
	case "receipt.read":
		return EventServerReceiptRead, true
	case "receipt.delivered":
		return EventServerReceiptDelivered, true
	case "conversation.created":
		return EventConvCreated, true
	case "conversation.membership":
		return EventMembership, true
	case "conversation.settings":
		return EventConvUpdated, true
	case "media.ready":
		return EventMediaReady, true
	case EventUserUpdated:
		return EventSettingsUpdated, true
	default:
		return "", false
	}
}
