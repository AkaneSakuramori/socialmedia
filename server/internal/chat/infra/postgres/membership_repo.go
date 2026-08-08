package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MembershipRepo is the pgx-backed domain.MembershipRepository
// (DATABASE.md §5.2).
type MembershipRepo struct {
	pool *pgxpool.Pool
}

// NewMembershipRepo builds the repository over the shared pool.
func NewMembershipRepo(pool *pgxpool.Pool) *MembershipRepo {
	return &MembershipRepo{pool: pool}
}

// membershipColumns is the SELECT projection for one membership row.
const membershipColumns = `
	conversation_id, user_id, role, last_read_seq, last_delivered_seq,
	last_read_at, muted_until, pinned_at, archived_at, joined_at, left_at`

// AddMany inserts membership rows within the given transaction.
func (r *MembershipRepo) AddMany(ctx context.Context, dbtx tx.Tx, ms []*domain.Membership) error {
	if len(ms) == 0 {
		return nil
	}
	values := make([]string, 0, len(ms))
	args := make([]any, 0, len(ms)*4)
	for i, m := range ms {
		base := i * 4
		values = append(values,
			`($`+itoa(base+1)+`,$`+itoa(base+2)+`,$`+itoa(base+3)+`,$`+itoa(base+4)+`)`)
		args = append(args, m.ConversationID, m.UserID, m.Role, m.JoinedAt)
	}
	_, err := dbtx.Exec(ctx,
		`INSERT INTO conversation_members (conversation_id, user_id, role, joined_at)
		 VALUES `+strings.Join(values, ",")+`
		 ON CONFLICT (conversation_id, user_id) DO UPDATE
		   SET left_at = NULL, role = EXCLUDED.role, joined_at = EXCLUDED.joined_at`,
		args...)
	if err != nil {
		return fmt.Errorf("chat: insert memberships: %w", err)
	}
	return nil
}

// FindActive loads the caller's current (not left) membership, or
// ErrMembershipNotFound.
func (r *MembershipRepo) FindActive(ctx context.Context, conversationID, userID int64) (*domain.Membership, error) {
	return scanMembership(r.pool.QueryRow(ctx,
		`SELECT `+membershipColumns+` FROM conversation_members
		 WHERE conversation_id = $1 AND user_id = $2 AND left_at IS NULL`,
		conversationID, userID))
}

// Update persists the mutable per-user fields.
func (r *MembershipRepo) Update(ctx context.Context, dbtx tx.Tx, m *domain.Membership) error {
	_, err := dbtx.Exec(ctx, `
		UPDATE conversation_members SET
			role = $3, last_read_seq = $4, last_delivered_seq = $5, last_read_at = $6,
			muted_until = $7, pinned_at = $8, archived_at = $9, left_at = $10
		WHERE conversation_id = $1 AND user_id = $2`,
		m.ConversationID, m.UserID, m.Role, m.LastReadSeq, m.LastDeliveredSeq,
		m.LastReadAt, m.MutedUntil, m.PinnedAt, m.ArchivedAt, m.LeftAt)
	if err != nil {
		return fmt.Errorf("chat: update membership: %w", err)
	}
	return nil
}

// Remove sets left_at on a membership (soft removal, kept for audit).
func (r *MembershipRepo) Remove(ctx context.Context, dbtx tx.Tx, conversationID, userID int64, leftAt time.Time) error {
	_, err := dbtx.Exec(ctx, `
		UPDATE conversation_members SET left_at = $3
		WHERE conversation_id = $1 AND user_id = $2 AND left_at IS NULL`,
		conversationID, userID, leftAt)
	if err != nil {
		return fmt.Errorf("chat: remove membership: %w", err)
	}
	return nil
}

// CountActive returns the number of current members.
func (r *MembershipRepo) CountActive(ctx context.Context, conversationID int64) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM conversation_members
		 WHERE conversation_id = $1 AND left_at IS NULL`, conversationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("chat: count members: %w", err)
	}
	return n, nil
}

// ActiveUserIDs returns the current member user ids (outbox fan-out list).
func (r *MembershipRepo) ActiveUserIDs(ctx context.Context, q tx.Querier, conversationID int64) ([]int64, error) {
	rows, err := q.Query(ctx,
		`SELECT user_id FROM conversation_members
		 WHERE conversation_id = $1 AND left_at IS NULL ORDER BY user_id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("chat: active user ids: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("chat: scan active user id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListMembers returns the paginated member list (API.md §7.5), keyset on
// joined_at DESC, user_id DESC, with an optional display-name substring filter.
func (r *MembershipRepo) ListMembers(ctx context.Context, conversationID int64, q domain.MemberListQuery) ([]domain.MemberRow, error) {
	query := `
		SELECT m.user_id, u.display_name, m.role, m.joined_at
		FROM conversation_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.conversation_id = $1 AND m.left_at IS NULL`
	args := []any{conversationID}

	if q.Q != "" {
		args = append(args, escapeLike(q.Q))
		query += ` AND u.display_name ILIKE '%' || $` + itoa(len(args)) + ` || '%'`
	}
	if q.Cursor != nil {
		args = append(args, q.Cursor.JoinedAt, q.Cursor.UserID)
		query += ` AND (m.joined_at, m.user_id) < ($` + itoa(len(args)-1) + `, $` + itoa(len(args)) + `)`
	}
	args = append(args, q.Limit)
	query += ` ORDER BY m.joined_at DESC, m.user_id DESC LIMIT $` + itoa(len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("chat: list members: %w", err)
	}
	defer rows.Close()

	out := make([]domain.MemberRow, 0, q.Limit)
	for rows.Next() {
		var row domain.MemberRow
		if err := rows.Scan(&row.UserID, &row.DisplayName, &row.Role, &row.JoinedAt); err != nil {
			return nil, fmt.Errorf("chat: scan member: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkRead advances the caller's read cursor(s) monotonically via GREATEST
// (API.md §10.1/§10.3): cursors can only advance, never regress. It returns
// whether each cursor actually moved, so the receipt.read outbox delta is only
// written for newly-read messages.
func (r *MembershipRepo) MarkRead(ctx context.Context, dbtx tx.Tx, conversationID, userID, readSeq, deliveredSeq int64, at time.Time) (advanceRead, advanceDelivered bool, err error) {
	// The data model forbids a read cursor ahead of the delivered cursor
	// (CHECK last_delivered_seq >= last_read_seq OR last_read_seq = 0,
	// migration 000006). Both cursors are therefore advanced in a single
	// statement — a read receipt implies delivery — so no intermediate state
	// ever violates the constraint. GREATEST keeps the advance monotonic.
	var oldRead, oldDelivered, newRead, newDelivered int64
	if err := dbtx.QueryRow(ctx,
		`SELECT last_read_seq, last_delivered_seq FROM conversation_members
		 WHERE conversation_id = $1 AND user_id = $2 AND left_at IS NULL`,
		conversationID, userID).Scan(&oldRead, &oldDelivered); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("chat: mark read: %w", err)
	}
	err = dbtx.QueryRow(ctx, `
		UPDATE conversation_members
		SET last_read_seq = GREATEST(last_read_seq, $3),
			last_delivered_seq = GREATEST(last_delivered_seq, $3, $5),
			last_read_at = $4
		WHERE conversation_id = $1 AND user_id = $2 AND left_at IS NULL
		RETURNING last_read_seq, last_delivered_seq`,
		conversationID, userID, readSeq, at, deliveredSeq).Scan(&newRead, &newDelivered)
	if err != nil {
		return false, false, fmt.Errorf("chat: mark read: %w", err)
	}
	return newRead > oldRead, newDelivered > oldDelivered, nil
}

// ListReceipts returns every active member's read cursor (API.md §10.2).
func (r *MembershipRepo) ListReceipts(ctx context.Context, conversationID int64) ([]domain.ReceiptRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, last_read_seq, last_read_at
		FROM conversation_members
		WHERE conversation_id = $1 AND left_at IS NULL
		ORDER BY user_id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("chat: list receipts: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ReceiptRow, 0, 4)
	for rows.Next() {
		var row domain.ReceiptRow
		if err := rows.Scan(&row.UserID, &row.LastReadSeq, &row.LastReadAt); err != nil {
			return nil, fmt.Errorf("chat: scan receipt: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CursorsByConversation loads all active members' cursors for history reads so
// per-message status/read_by can be derived in one fetch.
func (r *MembershipRepo) CursorsByConversation(ctx context.Context, conversationID int64) ([]domain.CursorRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, last_read_seq, last_delivered_seq
		FROM conversation_members
		WHERE conversation_id = $1 AND left_at IS NULL
		ORDER BY user_id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("chat: cursors by conversation: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CursorRow, 0, 4)
	for rows.Next() {
		var row domain.CursorRow
		if err := rows.Scan(&row.UserID, &row.LastReadSeq, &row.LastDeliveredSeq); err != nil {
			return nil, fmt.Errorf("chat: scan cursor: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanMembership maps one membership row to the domain value.
func scanMembership(row tx.Row) (*domain.Membership, error) {
	var m domain.Membership
	err := row.Scan(
		&m.ConversationID, &m.UserID, &m.Role, &m.LastReadSeq, &m.LastDeliveredSeq,
		&m.LastReadAt, &m.MutedUntil, &m.PinnedAt, &m.ArchivedAt, &m.JoinedAt, &m.LeftAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMembershipNotFound
		}
		return nil, fmt.Errorf("chat: scan membership: %w", err)
	}
	return &m, nil
}

// itoa converts an index to its SQL placeholder string.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// escapeLike neutralizes LIKE wildcards in a user-supplied substring filter.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
