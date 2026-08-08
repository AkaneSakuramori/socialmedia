// Package application implements the chat module's use-cases (ENGINEERING.md
// §7, §10). The exported Service interface is the only entry point other
// modules (delivery, or other application layers) may call. Services hold
// small ports from the domain layer, injected at the composition root; no I/O
// and no concrete adapters live here.
package application

import (
	"context"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/clock"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// Limits from the API surface (API.md §7.2/§7.6, §2.6).
const (
	// defaultLimit is the pagination default (API.md §2.6).
	defaultLimit = 50
	// maxLimit is the server-clamped page size (API.md §2.6).
	maxLimit = 100
	// maxTotalMembers caps a conversation's active membership at 500
	// (API.md §7.6 "max 500 total members").
	maxTotalMembers = 500
	// memberPreviewLimit is how many members the conversation detail returns
	// for the group avatar collage (API.md §7.3, "first 8").
	memberPreviewLimit = 8
)

// Service is the exported chat application service interface. The delivery
// layer depends on this, never on the concrete service.
type Service interface {
	// CreateConversation creates a direct or group conversation (API.md §7.2).
	// Direct conversations dedupe: an existing direct chat with the same
	// counterpart is returned with Created=false (HTTP 200) instead of a
	// duplicate (HTTP 201).
	CreateConversation(ctx context.Context, cmd CreateConversationCommand) (*CreateConversationResult, error)
	// ListConversations returns the caller's chat list (API.md §7.1),
	// most-recent-first, with derived unread counts and per-user prefs.
	ListConversations(ctx context.Context, cmd ListConversationsCommand) (*ConversationListResult, error)
	// GetConversation returns one conversation's metadata + membership summary
	// (API.md §7.3).
	GetConversation(ctx context.Context, cmd GetConversationCommand) (*ConversationDetail, error)
	// UpdateConversation updates group settings (API.md §7.4). Only owner/admin
	// roles may mutate a conversation.
	UpdateConversation(ctx context.Context, cmd UpdateConversationCommand) (*ConversationDetail, error)
	// ListMembers returns the paginated member list with roles (API.md §7.5).
	ListMembers(ctx context.Context, cmd ListMembersCommand) (*MemberListResult, error)
	// AddMembers adds users to a group (API.md §7.6), reporting per-user
	// added/skipped outcomes.
	AddMembers(ctx context.Context, cmd AddMembersCommand) (*AddMembersResult, error)
	// RemoveMember removes a member, or lets the caller leave (API.md §7.7).
	RemoveMember(ctx context.Context, cmd RemoveMemberCommand) error
	// ChangeMemberRole changes a member's role (API.md §7.8). Only the owner
	// may grant/revoke the owner role.
	ChangeMemberRole(ctx context.Context, cmd ChangeMemberRoleCommand) (*RoleChangeResult, error)
	// SetMute mutes/unmutes notifications for the caller (API.md §7.9).
	SetMute(ctx context.Context, cmd SetMuteCommand) (*MuteResult, error)
	// SetPin pins/unpins the conversation for the caller (API.md §7.10).
	SetPin(ctx context.Context, cmd SetPinCommand) (*PinResult, error)
	// SetArchive archives/unarchives the conversation for the caller
	// (API.md §7.11).
	SetArchive(ctx context.Context, cmd SetArchiveCommand) (*ArchiveResult, error)
	// SendMessage persists a message atomically (API.md §8.2): sequence
	// allocation, the message row, the monotonic conversation bump, and the
	// change_log outbox commit in one transaction. Idempotent: a retry with the
	// same client_msg_id returns the original message (Created=false → HTTP
	// 200). Exactly-once intent is enforced by the partial unique index even if
	// the HTTP idempotency cache is lost.
	SendMessage(ctx context.Context, cmd SendMessageCommand) (*SendMessageResult, error)
	// ListMessages paginates message history (API.md §8.1), keyset on sequence
	// (scroll-back) or global_seq (delta poll). Strictly ordered output.
	ListMessages(ctx context.Context, cmd ListMessagesCommand) (*MessageListResult, error)
	// GetMessage fetches one message by id (API.md §8.3), gated on membership.
	GetMessage(ctx context.Context, cmd GetMessageCommand) (*MessageView, error)
	// EditMessage edits a message within the sender-only edit window
	// (API.md §8.4). Edits are append-only (message_edits); concurrent edits
	// both record and last-write-wins on the visible body.
	EditMessage(ctx context.Context, cmd EditMessageCommand) (*MessageView, error)
	// DeleteMessage deletes a message (API.md §8.5). mode=all tombstones the
	// row (never re-used sequence slot); mode=self is client-local in v1 and a
	// no-op on the server.
	DeleteMessage(ctx context.Context, cmd DeleteMessageCommand) (*DeleteMessageResult, error)
	// AddReaction adds a reaction (API.md §8.6); a duplicate is a no-op 200.
	AddReaction(ctx context.Context, cmd ReactionCommand) (*ReactionResult, error)
	// RemoveReaction removes the caller's reaction (API.md §8.7).
	RemoveReaction(ctx context.Context, cmd ReactionCommand) (*ReactionResult, error)
	// ListReactions lists the reactors for a message + emoji (API.md §8.8).
	ListReactions(ctx context.Context, cmd ListReactionsCommand) (*ReactionsResult, error)
	// MarkRead advances the caller's read/delivered cursors monotonically
	// (API.md §10.1/§10.3; §7.12 shares the endpoint).
	MarkRead(ctx context.Context, cmd MarkReadCommand) (*ReceiptResult, error)
	// GetReceipts returns the per-member read state (API.md §10.2).
	GetReceipts(ctx context.Context, cmd GetReceiptsCommand) (*ReceiptsResult, error)
}

// CreateConversationCommand is the validated input for §7.2. ParticipantIDs
// excludes the caller. Direct takes exactly one other user; a group takes 1–499
// others (2–500 total members).
type CreateConversationCommand struct {
	UserID         int64
	Type           string
	ParticipantIDs []int64
	Title          *string // group only, required by DATABASE.md §5.1
	AvatarMediaID  *int64  // group only
}

// CreateConversationResult carries the created (or existing) conversation.
// Created=false means an existing direct conversation was returned (HTTP 200).
type CreateConversationResult struct {
	View    ConversationView
	Created bool
}

// ListConversationsCommand is the chat-list input (API.md §7.1 query). Filter
// is one of all|pinned|archived|groups|direct (default all).
type ListConversationsCommand struct {
	UserID     int64
	Filter     string
	UnreadOnly bool
	Limit      int
	Cursor     string
}

// ConversationListResult is a page of the chat list.
type ConversationListResult struct {
	Items   []ConversationView
	Next    *string
	HasMore bool
	Limit   int
}

// GetConversationCommand identifies one conversation for §7.3.
type GetConversationCommand struct {
	UserID         int64
	ConversationID int64
}

// UpdateConversationCommand is the §7.4 patch. Presence flags distinguish "not
// provided" from "cleared": TitleSet, AvatarSet/AvatarCleared, Settings.
type UpdateConversationCommand struct {
	UserID         int64
	ConversationID int64
	Title          *string
	TitleSet       bool
	AvatarMediaID  *int64
	AvatarSet      bool
	AvatarCleared  bool
	Settings       *SettingsPatch
}

// SettingsPatch is the optional settings update for §7.4.
type SettingsPatch struct {
	SlowModeSeconds *int
	AnyoneCanAdd    *bool
	HistoryVisible  *string
}

// ListMembersCommand is the §7.5 paginated member list input. Q is an optional
// display-name substring filter.
type ListMembersCommand struct {
	UserID         int64
	ConversationID int64
	Limit          int
	Cursor         string
	Q              string
}

// MemberListResult is a page of the member list.
type MemberListResult struct {
	Items   []MemberView
	Next    *string
	HasMore bool
	Limit   int
}

// AddMembersCommand adds UserIDs to a group (API.md §7.6).
type AddMembersCommand struct {
	UserID         int64
	ConversationID int64
	UserIDs        []int64
}

// AddMembersResult reports the per-user outcome (partial success is 200).
type AddMembersResult struct {
	Added   []string        `json:"added"`
	Skipped []SkippedMember `json:"skipped"`
}

// SkippedMember explains why a user was not added.
type SkippedMember struct {
	UserID string `json:"user_id"`
	Reason string `json:"reason"`
}

// RemoveMemberCommand removes a target member, or lets the caller leave when
// TargetUserID equals UserID (API.md §7.7).
type RemoveMemberCommand struct {
	UserID         int64
	ConversationID int64
	TargetUserID   int64
}

// RoleChangeResult echoes the updated membership after §7.8 (API.md §7.8
// response 200: updated membership).
type RoleChangeResult struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// ChangeMemberRoleCommand changes a member's role (API.md §7.8).
type ChangeMemberRoleCommand struct {
	UserID         int64
	ConversationID int64
	TargetUserID   int64
	Role           string
}

// SetMuteCommand mutes (Until set) or unmutes (Until nil) the conversation for
// the caller (API.md §7.9).
type SetMuteCommand struct {
	UserID         int64
	ConversationID int64
	Until          *time.Time
}

// MuteResult echoes the effective mute deadline.
type MuteResult struct {
	MutedUntil *time.Time `json:"muted_until"`
}

// SetPinCommand pins/unpins for the caller (API.md §7.10).
type SetPinCommand struct {
	UserID         int64
	ConversationID int64
	Pinned         bool
}

// PinResult echoes the pin state.
type PinResult struct {
	IsPinned bool `json:"is_pinned"`
}

// SetArchiveCommand archives/unarchives for the caller (API.md §7.11).
type SetArchiveCommand struct {
	UserID         int64
	ConversationID int64
	Archived       bool
}

// ArchiveResult echoes the archive state.
type ArchiveResult struct {
	IsArchived bool `json:"is_archived"`
}

// SendMessageCommand is the validated input for §8.2. Exactly one of Text or
// Media is set. Mentions must be conversation members (validated server-side).
type SendMessageCommand struct {
	UserID         int64
	ConversationID int64
	ClientMsgID    string
	Type           string
	Text           *string
	Media          []domain.Attachment
	ReplyToSeq     *int64
	Mentions       []int64
}

// SendMessageResult carries the persisted (or replayed) message. Created=false
// means an idempotent replay returned the original message (HTTP 200).
type SendMessageResult struct {
	View    MessageView
	Created bool
}

// ListMessagesCommand is the §8.1 history/delta seek. Cursor is the opaque
// next_cursor from a previous page (authoritative when present); BeforeSeq
// (scroll-back) and AfterGlobalSeq (delta poll) come from the raw query
// params. Neither seek opens the newest page.
type ListMessagesCommand struct {
	UserID         int64
	ConversationID int64
	Cursor         string
	BeforeSeq      *int64
	AfterGlobalSeq *int64
	Limit          int
}

// MessageListResult is a page of message history/delta.
type MessageListResult struct {
	Items   []MessageView
	Next    *string
	HasMore bool
	Limit   int
}

// GetMessageCommand identifies one message for §8.3.
type GetMessageCommand struct {
	UserID    int64
	MessageID int64
}

// EditMessageCommand is the §8.4 patch (content only; type/media never change).
type EditMessageCommand struct {
	UserID    int64
	MessageID int64
	NewText   string
}

// DeleteMessageCommand is the §8.5 delete. Mode is "all" (tombstone) or "self"
// (client-local no-op in v1).
type DeleteMessageCommand struct {
	UserID    int64
	MessageID int64
	Mode      string
}

// DeleteMessageResult echoes the applied delete (API.md §8.5 response 200).
type DeleteMessageResult struct {
	Deleted   string `json:"deleted"`
	MessageID string `json:"message_id"`
}

// ReactionCommand adds/removes one (message, user, emoji) reaction (§8.6/§8.7).
type ReactionCommand struct {
	UserID    int64
	MessageID int64
	Emoji     string
}

// ReactionResult echoes the reaction state (API.md §8.6/§8.7 response 200).
type ReactionResult struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
	Count     int64  `json:"count"`
}

// ListReactionsCommand lists the reactors for §8.8.
type ListReactionsCommand struct {
	UserID    int64
	MessageID int64
	Emoji     string
}

// ReactionsResult is the §8.8 reactor list.
type ReactionsResult struct {
	Emoji    string        `json:"emoji"`
	Reactors []ReactorView `json:"reactors"`
}

// ReactorView is one §8.8 reactor entry.
type ReactorView struct {
	UserID      string      `json:"user_id"`
	DisplayName string      `json:"display_name"`
	Avatar      *AvatarView `json:"avatar"`
	At          time.Time   `json:"at"`
}

// MarkReadCommand is the §10.1/§10.3 cursor advance. DeliveredSeq is optional
// (deliver_up_to_seq).
type MarkReadCommand struct {
	UserID         int64
	ConversationID int64
	ReadSeq        int64
	DeliveredSeq   *int64
}

// ReceiptResult echoes the effective cursors (API.md §10.1 response 200).
type ReceiptResult struct {
	LastReadSeq      string `json:"last_read_seq"`
	LastDeliveredSeq string `json:"last_delivered_seq"`
}

// GetReceiptsCommand fetches the per-member read state (§10.2).
type GetReceiptsCommand struct {
	UserID         int64
	ConversationID int64
}

// ReceiptsResult is the §10.2 readers[] response.
type ReceiptsResult struct {
	ConversationID string       `json:"conversation_id"`
	LastMessageSeq *string      `json:"last_message_seq"`
	Readers        []ReaderView `json:"readers"`
}

// ReaderView is one §10.2 reader entry.
type ReaderView struct {
	UserID      string     `json:"user_id"`
	DisplayName string     `json:"display_name"`
	LastReadSeq string     `json:"last_read_seq"`
	LastReadAt  *time.Time `json:"last_read_at"`
}

// MessageView is the message shape (API.md §8.1). All ids are strings.
type MessageView struct {
	ID             string              `json:"id"`
	ConversationID string              `json:"conversation_id"`
	Sequence       string              `json:"sequence"`
	SenderID       *string             `json:"sender_id"`
	Sender         *SenderView         `json:"sender"`
	Type           string              `json:"type"`
	Content        *MessageText        `json:"content"`
	Media          []domain.Attachment `json:"media"`
	ClientMsgID    *string             `json:"client_msg_id"`
	CreatedAt      time.Time           `json:"created_at"`
	EditedAt       *time.Time          `json:"edited_at"`
	Status         string              `json:"status"`
	ReplyTo        *ReplyToView        `json:"reply_to"`
	Mentions       []string            `json:"mentions"`
	Reactions      []domain.Reaction   `json:"reactions"`
	ReadBy         []ReadByView        `json:"read_by"`
	GlobalSeq      string              `json:"global_seq"`
}

// SenderView is the §8.1 sender object.
type SenderView struct {
	DisplayName string      `json:"display_name"`
	Avatar      *AvatarView `json:"avatar"`
}

// MessageText is the §8.1 content object.
type MessageText struct {
	Text *string `json:"text"`
}

// ReplyToView is the §8.1 reply_to object.
type ReplyToView struct {
	ID       string      `json:"id"`
	SenderID *string     `json:"sender_id"`
	Content  MessageText `json:"content"`
}

// ReadByView is one §8.1 read_by entry.
type ReadByView struct {
	UserID string     `json:"user_id"`
	At     *time.Time `json:"at"`
}

// ConversationView is the chat-list item shape (API.md §7.1). All ids are
// serialized as strings (API.md §2.2).
type ConversationView struct {
	ID             string           `json:"id"`
	Type           string           `json:"type"`
	Title          *string          `json:"title"`
	Avatar         *AvatarView      `json:"avatar"`
	LastMessage    *LastMessageView `json:"last_message"`
	LastMessageSeq *string          `json:"last_message_seq"`
	LastReadSeq    string           `json:"last_read_seq"`
	UnreadCount    int64            `json:"unread_count"`
	MutedUntil     *time.Time       `json:"muted_until"`
	IsPinned       bool             `json:"is_pinned"`
	IsArchived     bool             `json:"is_archived"`
	Membership     MembershipView   `json:"membership"`
	Typing         []string         `json:"typing"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// ConversationDetail is the single-conversation shape (API.md §7.3).
type ConversationDetail struct {
	ID             string               `json:"id"`
	Type           string               `json:"type"`
	Title          *string              `json:"title"`
	Avatar         *AvatarView          `json:"avatar"`
	OwnerID        string               `json:"owner_id"`
	CreatedAt      time.Time            `json:"created_at"`
	LastMessageSeq *string              `json:"last_message_seq"`
	Membership     DetailMembershipView `json:"membership"`
	Settings       SettingsView         `json:"settings"`
	MemberCount    int64                `json:"member_count"`
	MemberPreview  []MemberPreviewItem  `json:"member_preview"`
}

// AvatarView references a media object (media module later; url is empty until
// signed URLs land).
type AvatarView struct {
	MediaID *string `json:"media_id"`
	URL     string  `json:"url"`
}

// LastMessageView is the chat-list preview of the newest message.
type LastMessageView struct {
	ID        *string             `json:"id"`
	Seq       string              `json:"seq"`
	Content   *MessageContentView `json:"content"`
	SenderID  *string             `json:"sender_id"`
	CreatedAt *time.Time          `json:"created_at"`
	Status    string              `json:"status"`
}

// MessageContentView holds the text body of a message preview.
type MessageContentView struct {
	Text *string `json:"text"`
}

// MembershipView is the caller's role/join info in a chat-list item.
type MembershipView struct {
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// DetailMembershipView is the caller's membership in a conversation detail.
type DetailMembershipView struct {
	Role                 string     `json:"role"`
	MutedUntil           *time.Time `json:"muted_until"`
	NotificationsEnabled bool       `json:"notifications_enabled"`
}

// SettingsView is the group settings surface (API.md §7.3/§7.4).
type SettingsView struct {
	RetentionDays   *int   `json:"retention_days"`
	SlowModeSeconds int    `json:"slow_mode_seconds"`
	AnyoneCanAdd    bool   `json:"anyone_can_add"`
	HistoryVisible  string `json:"history_visible"`
}

// MemberPreviewItem is one entry of the group avatar collage (API.md §7.3).
type MemberPreviewItem struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// MemberView is one member-list row (API.md §7.5).
type MemberView struct {
	UserID       string      `json:"user_id"`
	DisplayName  string      `json:"display_name"`
	Avatar       *AvatarView `json:"avatar"`
	Role         string      `json:"role"`
	JoinedAt     time.Time   `json:"joined_at"`
	LastActiveAt *time.Time  `json:"last_active_at"` // null until presence (M4)
	IsSelf       bool        `json:"is_self"`
}

// Deps is the constructor-injected dependency set for the chat service.
type Deps struct {
	Conversations  domain.ConversationRepository
	Memberships    domain.MembershipRepository
	Sequences      domain.SequenceRepository
	Messages       domain.MessageRepository
	Reactions      domain.ReactionRepository
	SequenceSource domain.SequenceSource
	ChangeLog      domain.ChangeLogRepository
	Users          userdomain.UserRepository
	IDs            domain.IDGenerator
	TxBeginner     tx.Beginner
	Clock          clock.Clock
	// DB is the pool-backed read surface for pre-transaction and post-commit
	// reads (the connection pool implements tx.Querier). Transactional fan-out
	// reads pass the open transaction instead, so a write tx never holds a
	// second pool connection (a pool-exhaustion deadlock hazard under load).
	DB tx.Querier
}

type service struct {
	deps Deps
}

// New builds the chat application service (constructor injection only).
func New(deps Deps) Service { return &service{deps: deps} }

// now is a thin indirection over the injected clock for readability.
func (s *service) now() time.Time { return s.deps.Clock.Now() }
