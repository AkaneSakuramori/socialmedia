// Package domain holds the chat module's aggregates and rules (ENGINEERING.md
// §7, §10). Conversation and Membership are the two aggregates (ARCHITECTURE.md
// §9.1); Mute/Pin are per-user state on the membership, not separate
// aggregates. No I/O and no framework types live here.
package domain

import (
	"time"
)

// ConversationType distinguishes a 1:1 direct chat from a multi-party group
// (DATABASE.md §5.1: conversations.type CHECK IN ('direct','group')).
type ConversationType string

const (
	ConversationDirect ConversationType = "direct"
	ConversationGroup  ConversationType = "group"
)

// ParseConversationType validates a wire value against the two allowed types.
func ParseConversationType(s string) (ConversationType, error) {
	switch s {
	case string(ConversationDirect):
		return ConversationDirect, nil
	case string(ConversationGroup):
		return ConversationGroup, nil
	}
	return "", ErrInvalidConversationType
}

// HistoryVisible controls how much history a newly added group member can see
// (API.md §7.4 settings.history_visible): "all" or "from_join".
type HistoryVisible string

const (
	HistoryVisibleAll      HistoryVisible = "all"
	HistoryVisibleFromJoin HistoryVisible = "from_join"
)

// Valid reports whether the value is a supported history-visibility setting.
func (h HistoryVisible) Valid() bool {
	return h == HistoryVisibleAll || h == HistoryVisibleFromJoin
}

// Settings is the group-only settings blob stored in conversations.settings
// (DATABASE.md §5.1, API.md §7.4). Direct conversations ignore it.
type Settings struct {
	SlowModeSeconds int            `json:"slow_mode_seconds"`
	AnyoneCanAdd    bool           `json:"anyone_can_add"`
	HistoryVisible  HistoryVisible `json:"history_visible"`
}

// DefaultSettings is the baseline group configuration (history visible to all).
func DefaultSettings() Settings {
	return Settings{HistoryVisible: HistoryVisibleAll}
}

// Conversation is the aggregate root of the chat module (DATABASE.md §5.1):
// one row per 1:1 or group conversation. The last_message_* fields are the
// deliberate denormalization that serves the chat list in a single indexed
// query; they are updated in the same transaction that inserts a message.
type Conversation struct {
	ID                 int64
	Type               ConversationType
	Title              *string
	PhotoMediaID       *int64
	Description        *string
	CreatedBy          int64
	LastMessageAt      *time.Time
	LastMessageSeq     *int64
	LastMessageSnippet *string
	LastSenderID       *int64
	Settings           Settings
	RetentionDays      *int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// LastActivity is the chat-list sort key: last message time when present,
// otherwise creation time. New conversations appear at the top until they get
// messages (API.md §7.1 "most-recent-first").
func (c *Conversation) LastActivity() time.Time {
	if c.LastMessageAt != nil {
		return *c.LastMessageAt
	}
	return c.CreatedAt
}
