package domain

import "time"

// Role is a member's role in a group (DATABASE.md §5.2 role CHECK IN
// ('owner','admin','member')). Direct conversations use 'member' for both
// parties; groups are created with the creator as 'owner'.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// ParseRole validates a wire value against the three allowed roles.
func ParseRole(s string) (Role, error) {
	switch s {
	case string(RoleOwner):
		return RoleOwner, nil
	case string(RoleAdmin):
		return RoleAdmin, nil
	case string(RoleMember):
		return RoleMember, nil
	}
	return "", ErrInvalidRole
}

// AtLeast reports whether the role has at least the authority of target.
// owner >= admin >= member.
func (r Role) AtLeast(target Role) bool {
	rank := map[Role]int{RoleMember: 0, RoleAdmin: 1, RoleOwner: 2}
	return rank[r] >= rank[target]
}

// Membership is a user's row in a conversation (DATABASE.md §5.2): role, the
// read/delivery receipt cursors, per-user prefs (mute/pin/archive), and the
// join/leave lifecycle. It doubles as the ReadWatermark aggregate's storage
// (ARCHITECTURE.md §9.1) — the receipt milestone only advances these cursors.
type Membership struct {
	ConversationID   int64
	UserID           int64
	Role             Role
	LastReadSeq      int64
	LastDeliveredSeq int64
	LastReadAt       *time.Time
	MutedUntil       *time.Time
	PinnedAt         *time.Time
	ArchivedAt       *time.Time
	JoinedAt         time.Time
	LeftAt           *time.Time
}

// Active reports whether the membership is current (not left/removed).
func (m *Membership) Active() bool { return m.LeftAt == nil }

// UnreadCount derives the unread message count from the cursors
// (API.md §7.1: last_message_seq - last_read_seq, never a stored column).
func (m *Membership) UnreadCount(lastMessageSeq *int64) int64 {
	if lastMessageSeq == nil {
		return 0
	}
	if n := *lastMessageSeq - m.LastReadSeq; n > 0 {
		return n
	}
	return 0
}

// MessageStatus derives the delivery status of a message from the receipt
// cursors (API.md §7.1 last_message.status): read >= delivered >= sent.
func (m *Membership) MessageStatus(seq *int64) string {
	if seq == nil {
		return ""
	}
	switch {
	case *seq <= m.LastReadSeq:
		return "read"
	case *seq <= m.LastDeliveredSeq:
		return "delivered"
	default:
		return "sent"
	}
}
