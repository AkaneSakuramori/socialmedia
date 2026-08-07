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
	Conversations domain.ConversationRepository
	Memberships   domain.MembershipRepository
	Sequences     domain.SequenceRepository
	ChangeLog     domain.ChangeLogRepository
	Users         userdomain.UserRepository
	IDs           domain.IDGenerator
	TxBeginner    tx.Beginner
	Clock         clock.Clock
}

type service struct {
	deps Deps
}

// New builds the chat application service (constructor injection only).
func New(deps Deps) Service { return &service{deps: deps} }

// now is a thin indirection over the injected clock for readability.
func (s *service) now() time.Time { return s.deps.Clock.Now() }
