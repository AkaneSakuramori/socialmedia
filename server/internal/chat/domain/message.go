package domain

import (
	"time"
)

// MessageType is the stored message kind (DATABASE.md §5.3 type CHECK).
// "deleted" is a rendered projection of a tombstoned message, not a stored
// type: the CHECK constraint forbids it, so the read path derives it from
// deleted_at (API.md §8.1 lists it among rendered types).
type MessageType string

const (
	MessageTypeText      MessageType = "text"
	MessageTypeMedia     MessageType = "media"
	MessageTypeSystem    MessageType = "system"
	MessageTypeReply     MessageType = "reply"
	MessageTypeForwarded MessageType = "forwarded"
)

// ParseMessageType validates a wire type against the allowed values. The wire
// type list (API.md §8.1) adds "deleted", which is derived at read time.
func ParseMessageType(s string) (MessageType, error) {
	switch s {
	case string(MessageTypeText):
		return MessageTypeText, nil
	case string(MessageTypeMedia):
		return MessageTypeMedia, nil
	case string(MessageTypeSystem):
		return MessageTypeSystem, nil
	case string(MessageTypeReply):
		return MessageTypeReply, nil
	case string(MessageTypeForwarded):
		return MessageTypeForwarded, nil
	}
	return "", ErrInvalidMessageType
}

// maxMessageTextLen bounds the text body (API.md §8.2: 1–4000 chars).
const MaxMessageTextLen = 4000

// maxMediaPerMessage bounds the attachment envelope (API.md §8.2: ≥1, ≤10).
const MaxMediaPerMessage = 10

// Message is the messaging aggregate root (DATABASE.md §5.3). Ordering is the
// per-conversation `sequence`; `GlobalSeq` is the cross-conversation sync feed
// order. The aggregate is value-typed; mutations are expressed through the
// repository's guarded updates (edit/delete), never by mutating this struct.
type Message struct {
	ID                 int64
	ConversationID     int64
	Sequence           int64
	ClientMsgID        *string
	SenderID           *int64 // NULL for system events
	Type               MessageType
	Content            *string
	AttachmentEnvelope []byte // jsonb passthrough (media milestone validates refs)
	Mentions           []int64
	ReplyToID          *int64
	EditCount          int
	EditedAt           *time.Time
	DeletedAt          *time.Time
	DeletedBy          *int64
	GlobalSeq          int64
	CreatedAt          time.Time
}

// Deleted reports whether the message is tombstoned (delete-for-all).
func (m *Message) Deleted() bool { return m.DeletedAt != nil }

// RenderedType is the wire type (API.md §8.1): a tombstoned message renders as
// "deleted" regardless of its stored type.
func (m *Message) RenderedType() MessageType {
	if m.Deleted() {
		return MessageTypeDeleted
	}
	return m.Type
}

// MessageTypeDeleted is the rendered tombstone type (not storable).
const MessageTypeDeleted MessageType = "deleted"

// EditWindow is the sender-only edit window (API.md §8.4: default 24 h,
// measured from the message's original creation).
const EditWindow = 24 * time.Hour

// Editable reports whether the message is within its edit window.
func (m *Message) Editable(now time.Time) bool {
	return !m.Deleted() && now.Before(m.CreatedAt.Add(EditWindow))
}

// SenderIDOrZero returns the sender id, or 0 for system messages.
func (m *Message) SenderIDOrZero() int64 {
	if m.SenderID == nil {
		return 0
	}
	return *m.SenderID
}

// SenderOf reports whether userID is this message's sender.
func (m *Message) SenderOf(userID int64) bool {
	return m.SenderID != nil && *m.SenderID == userID
}

// ReplyTo is the rendered reply reference (API.md §8.1 reply_to).
type ReplyTo struct {
	ID       string
	SenderID *string
	Text     *string
	Type     MessageType
}

// Attachment is one entry of the media envelope (API.md §8.1 media[]).
type Attachment struct {
	MediaID string `json:"media_id"`
	Kind    string `json:"kind"`
	URL     string `json:"url,omitempty"`
	Thumb   string `json:"thumb,omitempty"`
	Size    *int64 `json:"size,omitempty"`
	Width   *int   `json:"width,omitempty"`
	Height  *int   `json:"height,omitempty"`
	Caption string `json:"caption,omitempty"`
}

// Reaction is one aggregated reaction row (API.md §8.1 reactions[]).
type Reaction struct {
	Emoji   string  `json:"emoji"`
	Count   int64   `json:"count"`
	UserIDs []int64 `json:"user_ids"`
}

// Reactor is one user who reacted with an emoji (API.md §8.8 reactors[]).
type Reactor struct {
	UserID      int64
	DisplayName string
	At          time.Time
}
