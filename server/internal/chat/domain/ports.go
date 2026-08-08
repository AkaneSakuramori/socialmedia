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
	// BumpLastMessage advances the denormalized last_message_* columns
	// monotonically (DATABASE.md §5.1/§10). The guard `last_message_seq < seq`
	// means an out-of-order commit (Redis INCR order != commit order) can never
	// regress the chat list to a stale message; returns whether it advanced.
	BumpLastMessage(ctx context.Context, dbtx tx.Tx, id, seq int64, snippet *string, senderID *int64, at time.Time) (bool, error)
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
	// The querier runs the read inside the caller's transaction when one is
	// open (never take a second pool connection while holding a write tx).
	ActiveUserIDs(ctx context.Context, q tx.Querier, conversationID int64) ([]int64, error)
	// ListMembers returns the paginated member list with display names
	// (API.md §7.5), keyset-paginated on joined_at DESC, user_id DESC.
	ListMembers(ctx context.Context, conversationID int64, q MemberListQuery) ([]MemberRow, error)
	// MarkRead advances the caller's read cursor(s) monotonically via GREATEST
	// (API.md §10.1, §10.3): the cursors can only advance, never regress. It
	// reports whether each cursor actually advanced (for the outbox delta).
	MarkRead(ctx context.Context, dbtx tx.Tx, conversationID, userID, readSeq, deliveredSeq int64, at time.Time) (advanceRead, advanceDelivered bool, err error)
	// ListReceipts returns every active member's read cursor for the §10.2
	// readers[] response.
	ListReceipts(ctx context.Context, conversationID int64) ([]ReceiptRow, error)
	// CursorsByConversation loads all active members' cursors so history reads
	// can derive per-message status/read_by in one fetch.
	CursorsByConversation(ctx context.Context, conversationID int64) ([]CursorRow, error)
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
	EventMessageCreated         = "message.created"
	EventMessageEdited          = "message.edited"
	EventMessageDeleted         = "message.deleted"
	EventMessageReaction        = "message.reaction"
	EventReceiptRead            = "receipt.read"
	EventReceiptDelivered       = "receipt.delivered"
)

// ChangeLogRepository owns persistence for change_log (DATABASE.md §7.1).
type ChangeLogRepository interface {
	// Append inserts outbox entries within the given transaction.
	Append(ctx context.Context, dbtx tx.Tx, entries []ChangeLogEntry) error
}

// SequenceSource allocates per-conversation message sequences (ARCHITECTURE.md
// §13.2, DATABASE.md §5.4). The Redis counter is the hot path (atomic INCR
// across instances); the durable PG row is the recovery ground truth and the
// fallback when Redis is unavailable. No row locks are taken on the hot path —
// the messages composite PK is the final guard against reuse.
type SequenceSource interface {
	// Next allocates the next sequence for a conversation. On Redis loss it
	// falls back to a single-row PG increment (serialized per conversation).
	// It may return a sequence already persisted only if the durable floor was
	// stale; the PK guard converts that into ErrSequenceConflict and the caller
	// retries.
	Next(ctx context.Context, conversationID int64) (int64, error)
	// Persist reconciles the durable floor inside the send transaction
	// (GREATEST max-merge, idempotent — DATABASE.md §5.4), so a Redis restart
	// can never reuse a sequence.
	Persist(ctx context.Context, dbtx tx.Tx, conversationID, sequence int64) error
	// Floor returns the durable counter (for sequence-source tests).
	Floor(ctx context.Context, conversationID int64) (int64, error)
}

// MessageRepository owns persistence for messages (DATABASE.md §5.3).
type MessageRepository interface {
	// Insert writes a message within the given transaction. On a retried send
	// the partial unique index (sender_id, client_msg_id) collapses the insert
	// (ON CONFLICT DO NOTHING): inserted=false means the caller re-selects the
	// original via FindByClientMsgID and returns it (idempotent replay,
	// API.md §8.2). A composite-PK collision surfaces as ErrSequenceConflict.
	Insert(ctx context.Context, dbtx tx.Tx, m *Message) (inserted bool, err error)
	// FindByClientMsgID loads the original message for an idempotent replay.
	// The querier is the caller's still-open transaction (see ActiveUserIDs).
	FindByClientMsgID(ctx context.Context, q tx.Querier, senderID int64, clientMsgID string) (*Message, error)
	// FindByID loads a message by its snowflake id, or ErrMessageNotFound.
	FindByID(ctx context.Context, id int64) (*Message, error)
	// FindByConversationSeq resolves a reply_to_seq reference, or
	// ErrMessageNotFound.
	FindByConversationSeq(ctx context.Context, conversationID, seq int64) (*Message, error)
	// ListByConversation returns a page of messages (API.md §8.1), keyset on
	// sequence (history scroll-back) or global_seq (delta poll), never by time.
	ListByConversation(ctx context.Context, q MessageListQuery) ([]Message, error)
	// Edit atomically records an edit: guarded UPDATE of content/edit_count/
	// edited_at (WHERE deleted_at IS NULL) + an append to message_edits, in the
	// given transaction. Returns false when the message is gone or tombstoned.
	Edit(ctx context.Context, dbtx tx.Tx, editID int64, m *Message, oldContent string, at time.Time) (bool, error)
	// Tombstone soft-deletes a message (API.md §8.5 mode=all): guarded UPDATE
	// WHERE deleted_at IS NULL. Returns false when already tombstoned or gone.
	Tombstone(ctx context.Context, dbtx tx.Tx, id, deletedBy int64, at time.Time) (bool, error)
	// SenderIDsBetween returns the distinct senders of messages with
	// from < sequence <= to in a conversation (the receipt.read fan-out list).
	SenderIDsBetween(ctx context.Context, dbtx tx.Tx, conversationID, from, to int64) ([]int64, error)
}

// MessageListQuery is the §8.1 keyset seek. At most one of BeforeSeq and
// AfterGlobalSeq may be set; neither means "newest page".
type MessageListQuery struct {
	ConversationID int64
	BeforeSeq      *int64
	AfterGlobalSeq *int64
	Limit          int
}

// MessageEdit is one append-only edit-history row (DATABASE.md §5.5).
type MessageEdit struct {
	ID         int64
	MessageID  int64
	EditedBy   int64
	OldContent string
	EditedAt   time.Time
}

// ReactionRow is one (message, user, emoji) reaction (DATABASE.md §5.6).
type ReactionRow struct {
	ID        int64
	MessageID int64
	UserID    int64
	Emoji     string
	CreatedAt time.Time
}

// ReactionRepository owns persistence for message_reactions (DATABASE.md §5.6).
// Counts are derived by GROUP BY, never stored.
type ReactionRepository interface {
	// Add inserts a reaction within the given transaction; duplicate
	// (message,user,emoji) is a no-op (returns false).
	Add(ctx context.Context, dbtx tx.Tx, r *ReactionRow) (added bool, err error)
	// Remove deletes the caller's reaction within the given transaction;
	// returns whether a row was actually removed.
	Remove(ctx context.Context, dbtx tx.Tx, messageID, userID int64, emoji string) (removed bool, err error)
	// DistinctEmoji returns how many distinct emoji a message already has
	// (API.md §8.6: max 20).
	DistinctEmoji(ctx context.Context, messageID int64) (int64, error)
	// Count returns the reactor count for a message + emoji.
	Count(ctx context.Context, messageID int64, emoji string) (int64, error)
	// CountsByMessages aggregates emoji counts for a page of messages
	// (message_id -> emoji -> count) in one query.
	CountsByMessages(ctx context.Context, messageIDs []int64) (map[int64]map[string]int64, error)
	// UserIDsByMessages aggregates the reactor user ids per (message, emoji)
	// for the rendered reaction chips (message_id -> emoji -> user ids).
	UserIDsByMessages(ctx context.Context, messageIDs []int64) (map[int64]map[string][]int64, error)
	// Reactors lists the reactors (user_id + at) for a message + emoji
	// (API.md §8.8), most recent first.
	Reactors(ctx context.Context, messageID int64, emoji string) ([]Reactor, error)
}

// ReceiptRow is one member's read cursor for the §10.2 readers[] response.
type ReceiptRow struct {
	UserID      int64
	LastReadSeq int64
	LastReadAt  *time.Time
}

// CursorRow is one member's receipt cursors, used to derive per-message
// status/read_by on history reads.
type CursorRow struct {
	UserID           int64
	LastReadSeq      int64
	LastDeliveredSeq int64
}

// IDGenerator mints snowflake-style 64-bit ids (internal/platform/idgen,
// ARCHITECTURE.md §2.2). Conversations and messages are the two id consumers.
type IDGenerator interface {
	NextID() (int64, error)
}
