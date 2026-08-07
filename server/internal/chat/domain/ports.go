package domain

import (
	"context"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// ConversationRepository owns persistence for conversations (DATABASE.md §5.1).
type ConversationRepository interface {
	// Create inserts a conversation within the given transaction.
	Create(ctx context.Context, dbtx tx.Tx, c *Conversation) error
	// FindByID loads a non-deleted conversation, or ErrConversationNotFound.
	FindByID(ctx context.Context, id int64) (*Conversation, error)
	// FindDirectPair returns the active direct conversation whose members are
	// exactly {userA, userB} (either direction), or ErrConversationNotFound.
	// Used for the §7.2 dedup: a second direct chat with the same counterpart
	// returns the existing one instead of creating a duplicate.
	FindDirectPair(ctx context.Context, userA, userB int64) (*Conversation, error)
	// Update persists the mutable conversation fields (title, photo, settings,
	// description, updated_at, denormalized last_message_* columns).
	Update(ctx context.Context, dbtx tx.Tx, c *Conversation) error
	// Tombstone soft-deletes a conversation whose last member left
	// (API.md §7.7: kept for history, flagged archived via deleted_at).
	Tombstone(ctx context.Context, dbtx tx.Tx, id int64, at time.Time) error
	// List returns the caller's chat list (API.md §7.1), keyset-paginated on
	// COALESCE(last_message_at, created_at) DESC, id DESC. Limit must already
	// be limit+1 so the caller can detect has_more.
	List(ctx context.Context, q ConversationListQuery) ([]ConversationRow, error)
}

// ConversationListQuery carries the chat-list filter and keyset seek
// (API.md §7.1 query: filter, unread_only, limit, cursor).
type ConversationListQuery struct {
	UserID     int64
	Filter     string // all | pinned | archived | groups | direct
	UnreadOnly bool
	Limit      int
	Cursor     *ConversationCursor
}

// ConversationCursor is the decoded keyset position of the chat list
// (COALESCE(last_message_at, created_at), id).
type ConversationCursor struct {
	Activity time.Time
	ID       int64
}

// ConversationRow is one chat-list row: the conversation, the caller's own
// membership (cursors/role/mute/pin/archive), and — for direct chats — the
// counterpart's user id so the title can be derived from their display name.
type ConversationRow struct {
	Conversation
	Membership    Membership
	CounterpartID *int64
}

// MembershipRepository owns persistence for conversation_members
// (DATABASE.md §5.2) — the home of roles, cursors, and per-user prefs.
type MembershipRepository interface {
	// AddMany inserts membership rows within the given transaction.
	AddMany(ctx context.Context, dbtx tx.Tx, ms []*Membership) error
	// FindActive loads the caller's current (not left) membership, or
	// ErrMembershipNotFound.
	FindActive(ctx context.Context, conversationID, userID int64) (*Membership, error)
	// Update persists the mutable per-user fields (role, cursors, mute/pin/
	// archive, timestamps).
	Update(ctx context.Context, dbtx tx.Tx, m *Membership) error
	// Remove sets left_at on a membership (soft removal, kept for audit).
	Remove(ctx context.Context, dbtx tx.Tx, conversationID, userID int64, leftAt time.Time) error
	// CountActive returns the number of current members (left_at IS NULL).
	CountActive(ctx context.Context, conversationID int64) (int64, error)
	// ActiveUserIDs returns the current member user ids (left_at IS NULL),
	// used to compute the outbox fan-out list for membership/settings events.
	ActiveUserIDs(ctx context.Context, conversationID int64) ([]int64, error)
	// ListMembers returns the paginated member list with display names
	// (API.md §7.5), keyset-paginated on joined_at DESC, user_id DESC.
	ListMembers(ctx context.Context, conversationID int64, q MemberListQuery) ([]MemberRow, error)
}

// MemberListQuery carries the member-list pagination and name filter
// (API.md §7.5 query: limit, cursor, q).
type MemberListQuery struct {
	Limit  int
	Cursor *MemberCursor
	Q      string
}

// MemberCursor is the decoded keyset position of the member list (joined_at,
// user_id).
type MemberCursor struct {
	JoinedAt time.Time
	UserID   int64
}

// MemberRow is one member-list row (API.md §7.5 data[] item, minus the fields
// the application derives from the user domain).
type MemberRow struct {
	UserID      int64
	DisplayName string
	Role        Role
	JoinedAt    time.Time
}

// SequenceRepository owns the durable per-conversation sequence counter
// (DATABASE.md §5.4, conversation_sequences). The Redis hot path lands with
// the messaging milestone; M1 only needs the row to exist so recovery ground
// truth starts at zero.
type SequenceRepository interface {
	// Init creates the counter row for a new conversation within the tx.
	Init(ctx context.Context, dbtx tx.Tx, conversationID int64) error
}

// ChangeLogEntry is one row of the transactional outbox / sync feed
// (DATABASE.md §7.1). Entries are written in the same transaction as the
// domain write; payload is a self-contained JSON event envelope.
type ChangeLogEntry struct {
	EventType       string
	ConversationID  *int64
	EntityID        *int64
	ActorUserID     *int64
	AffectedUserIDs []int64
	Payload         []byte
}

// ChangeLog event types owned by the chat module (DATABASE.md §7.1 CHECK).
const (
	EventConversationCreated    = "conversation.created"
	EventConversationMembership = "conversation.membership"
	EventConversationSettings   = "conversation.settings"
)

// ChangeLogRepository owns persistence for change_log (DATABASE.md §7.1).
type ChangeLogRepository interface {
	// Append inserts outbox entries within the given transaction.
	Append(ctx context.Context, dbtx tx.Tx, entries []ChangeLogEntry) error
}

// IDGenerator mints snowflake-style 64-bit ids (internal/platform/idgen,
// ARCHITECTURE.md §2.2). Conversations and messages are the two id consumers.
type IDGenerator interface {
	NextID() (int64, error)
}
