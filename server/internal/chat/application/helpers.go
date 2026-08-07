package application

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// makeErr wraps an unexpected error with operation context (ENGINEERING.md
// §14.3: the cause chain is preserved for logging).
func makeErr(err error, detail string) error { return fmt.Errorf("%s: %w", detail, err) }

// Cursors are opaque, versioned, base64url-encoded keyset positions
// (API.md §2.6: "cursors are opaque to clients"). Layout version 1:
// 1 version byte | 8 bytes sort timestamp (unix micro, UTC) | 8 bytes id.
// The sort value and id must both fit in int64 (they are snowflake ids and
// timestamps).

func encodeCursor(version byte, at time.Time, id int64) (string, error) {
	buf := make([]byte, 17)
	buf[0] = version
	binary.BigEndian.PutUint64(buf[1:9], uint64(at.UnixMicro()))
	binary.BigEndian.PutUint64(buf[9:17], uint64(id))
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func decodeCursor(version byte, s string) (time.Time, int64, error) {
	if s == "" {
		return time.Time{}, 0, domain.ErrInvalidCursor
	}
	buf, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(buf) != 17 || buf[0] != version {
		return time.Time{}, 0, domain.ErrInvalidCursor
	}
	at := time.UnixMicro(int64(binary.BigEndian.Uint64(buf[1:9]))).UTC()
	id := int64(binary.BigEndian.Uint64(buf[9:17]))
	return at, id, nil
}

// conversationCursor encodes the chat-list keyset position
// (COALESCE(last_message_at, created_at), id).
func conversationCursor(c domain.ConversationCursor) (string, error) {
	return encodeCursor(1, c.Activity, c.ID)
}

func decodeConversationCursor(s string) (*domain.ConversationCursor, error) {
	if s == "" {
		return nil, nil // no cursor → start at the top of the sort
	}
	at, id, err := decodeCursor(1, s)
	if err != nil {
		return nil, err
	}
	return &domain.ConversationCursor{Activity: at, ID: id}, nil
}

// memberCursor encodes the member-list keyset position (joined_at, user_id).
func memberCursor(c domain.MemberCursor) (string, error) {
	return encodeCursor(2, c.JoinedAt, c.UserID)
}

func decodeMemberCursor(s string) (*domain.MemberCursor, error) {
	if s == "" {
		return nil, nil // no cursor → start at the top of the sort
	}
	at, id, err := decodeCursor(2, s)
	if err != nil {
		return nil, err
	}
	return &domain.MemberCursor{JoinedAt: at, UserID: id}, nil
}

// validationError builds a field-level validation failure (API.md §2.5
// errors[]; the delivery layer maps it to 422 VALIDATION_ERROR).
func validationError(field, reason string) error {
	return &domain.ValidationError{Field: field, Reason: reason}
}

// newMembership builds a fresh membership row for a new conversation.
func newMembership(conversationID, userID int64, role domain.Role, now time.Time) *domain.Membership {
	return &domain.Membership{
		ConversationID: conversationID,
		UserID:         userID,
		Role:           role,
		JoinedAt:       now,
	}
}

// validateParticipants checks the §7.2 participant rules and returns the
// caller-excluded, de-duplicated list of other participants.
func (s *service) validateParticipants(ctx context.Context, caller int64, ctype domain.ConversationType, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, validationError("participant_ids", "must_not_be_empty")
	}
	seen := make(map[int64]bool, len(ids))
	var others []int64
	for _, id := range ids {
		if id <= 0 {
			return nil, validationError("participant_ids", "invalid_user_id")
		}
		if id == caller {
			return nil, validationError("participant_ids", "must_not_include_self")
		}
		if seen[id] {
			return nil, validationError("participant_ids", "must_be_unique")
		}
		seen[id] = true
		others = append(others, id)
	}

	switch ctype {
	case domain.ConversationDirect:
		if len(others) != 1 {
			return nil, validationError("participant_ids", "direct_requires_exactly_one_other")
		}
	case domain.ConversationGroup:
		if len(others) > maxTotalMembers-1 {
			return nil, validationError("participant_ids", "too_many_participants")
		}
	default:
		return nil, domain.ErrInvalidConversationType
	}

	// Every participant must be a live account (deleted accounts are omitted
	// by the repository, so a short count means an unknown/deleted user).
	users, err := s.deps.Users.ListByIDs(ctx, others)
	if err != nil {
		return nil, err
	}
	found := make(map[int64]bool, len(users))
	for _, u := range users {
		found[u.ID] = true
	}
	for _, id := range others {
		if !found[id] {
			return nil, validationError("participant_ids", "unknown_user")
		}
	}
	return others, nil
}

// displayNames builds the id → display_name map for a set of users.
func displayNames(users []userdomain.User) map[int64]string {
	out := make(map[int64]string, len(users))
	for _, u := range users {
		out[u.ID] = u.DisplayName
	}
	return out
}

// avatarView renders a media reference, or nil when absent (media milestone
// later; signed urls are out of scope).
func avatarView(mediaID *int64) *AvatarView {
	if mediaID == nil {
		return nil
	}
	id := strconv.FormatInt(*mediaID, 10)
	return &AvatarView{MediaID: &id}
}

// seqString formats an optional sequence value as a string (API.md §2.2).
func seqString(seq *int64) *string {
	if seq == nil {
		return nil
	}
	s := strconv.FormatInt(*seq, 10)
	return &s
}

// lastMessageView renders the chat-list preview of the newest message from the
// denormalized conversations columns (DATABASE.md §5.1). The message id is not
// stored on the conversation row and is resolved by the messaging milestone.
func lastMessageView(c domain.Conversation, m domain.Membership) *LastMessageView {
	if c.LastMessageSeq == nil {
		return nil
	}
	v := &LastMessageView{
		Seq:       strconv.FormatInt(*c.LastMessageSeq, 10),
		Content:   &MessageContentView{Text: c.LastMessageSnippet},
		CreatedAt: c.LastMessageAt,
		Status:    m.MessageStatus(c.LastMessageSeq),
	}
	if c.LastSenderID != nil {
		sender := strconv.FormatInt(*c.LastSenderID, 10)
		v.SenderID = &sender
	}
	return v
}

// buildListView renders a chat-list item (API.md §7.1). names resolves
// counterpart display names for direct titles.
func (s *service) buildListView(row domain.ConversationRow, names map[int64]string) ConversationView {
	c := row.Conversation
	m := row.Membership

	v := ConversationView{
		ID:             strconv.FormatInt(c.ID, 10),
		Type:           string(c.Type),
		Avatar:         avatarView(c.PhotoMediaID),
		LastMessage:    lastMessageView(c, m),
		LastMessageSeq: seqString(c.LastMessageSeq),
		LastReadSeq:    strconv.FormatInt(m.LastReadSeq, 10),
		UnreadCount:    m.UnreadCount(c.LastMessageSeq),
		MutedUntil:     m.MutedUntil,
		IsPinned:       m.PinnedAt != nil,
		IsArchived:     m.ArchivedAt != nil,
		Membership:     MembershipView{Role: string(m.Role), JoinedAt: m.JoinedAt},
		Typing:         []string{}, // typing lands with presence (M4)
		UpdatedAt:      c.UpdatedAt,
	}

	if c.Type == domain.ConversationDirect {
		if row.CounterpartID != nil {
			name := names[*row.CounterpartID]
			v.Title = &name
		}
	} else {
		v.Title = c.Title
	}
	return v
}

// buildDetailView renders the single-conversation shape (API.md §7.3).
func (s *service) buildDetailView(c domain.Conversation, m domain.Membership, memberCount int64, preview []MemberPreviewItem) ConversationDetail {
	return ConversationDetail{
		ID:             strconv.FormatInt(c.ID, 10),
		Type:           string(c.Type),
		Title:          c.Title,
		Avatar:         avatarView(c.PhotoMediaID),
		OwnerID:        strconv.FormatInt(c.CreatedBy, 10),
		CreatedAt:      c.CreatedAt,
		LastMessageSeq: seqString(c.LastMessageSeq),
		Membership: DetailMembershipView{
			Role:                 string(m.Role),
			MutedUntil:           m.MutedUntil,
			NotificationsEnabled: m.MutedUntil == nil || m.MutedUntil.After(s.now()),
		},
		Settings: SettingsView{
			RetentionDays:   c.RetentionDays,
			SlowModeSeconds: c.Settings.SlowModeSeconds,
			AnyoneCanAdd:    c.Settings.AnyoneCanAdd,
			HistoryVisible:  string(c.Settings.HistoryVisible),
		},
		MemberCount:   memberCount,
		MemberPreview: preview,
	}
}
