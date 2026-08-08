// Package postgres implements the chat module repositories over PostgreSQL
// (DATABASE.md §5.1, §5.2, §5.4, §7.1). The chat list is a single indexed
// query per user against conversations ⋈ conversation_members; unread counts
// are derived in SQL, never stored.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConversationRepo is the pgx-backed domain.ConversationRepository.
type ConversationRepo struct {
	pool *pgxpool.Pool
}

// NewConversationRepo builds the repository over the shared pool.
func NewConversationRepo(pool *pgxpool.Pool) *ConversationRepo {
	return &ConversationRepo{pool: pool}
}

// conversationColumns is the SELECT projection shared by every conversation
// lookup.
const conversationColumns = `
	id, type, title, photo_media_id, description, created_by,
	last_message_at, last_message_seq, last_message_snippet, last_sender_id,
	settings, retention_days, created_at, updated_at, deleted_at`

// Create inserts a conversation within the given transaction.
func (r *ConversationRepo) Create(ctx context.Context, dbtx tx.Tx, c *domain.Conversation) error {
	settingsJSON, err := json.Marshal(c.Settings)
	if err != nil {
		return fmt.Errorf("chat: marshal settings: %w", err)
	}
	_, err = dbtx.Exec(ctx, `
		INSERT INTO conversations (
			id, type, title, photo_media_id, description, created_by,
			settings, retention_days, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		c.ID, c.Type, c.Title, c.PhotoMediaID, c.Description, c.CreatedBy,
		settingsJSON, c.RetentionDays, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("chat: insert conversation: %w", err)
	}
	return nil
}

// FindByID loads a non-deleted conversation, or ErrConversationNotFound.
func (r *ConversationRepo) FindByID(ctx context.Context, id int64) (*domain.Conversation, error) {
	return scanConversation(r.pool.QueryRow(ctx,
		`SELECT `+conversationColumns+` FROM conversations
		 WHERE id = $1 AND deleted_at IS NULL`, id))
}

// FindDirectPair returns the active direct conversation whose member set is
// exactly {userA, userB} (either direction), or ErrConversationNotFound. The
// NOT EXISTS clause rejects conversations with any other member, so a group
// never matches.
func (r *ConversationRepo) FindDirectPair(ctx context.Context, userA, userB int64) (*domain.Conversation, error) {
	return scanConversation(r.pool.QueryRow(ctx, `
		SELECT `+conversationColumns+`
		FROM conversations c
		WHERE c.type = 'direct' AND c.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM conversation_members a
		              WHERE a.conversation_id = c.id AND a.user_id = $1 AND a.left_at IS NULL)
		  AND EXISTS (SELECT 1 FROM conversation_members b
		              WHERE b.conversation_id = c.id AND b.user_id = $2 AND b.left_at IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM conversation_members x
		                  WHERE x.conversation_id = c.id AND x.user_id NOT IN ($1,$2))
		LIMIT 1`, userA, userB))
}

// Update persists the mutable conversation fields.
func (r *ConversationRepo) Update(ctx context.Context, dbtx tx.Tx, c *domain.Conversation) error {
	settingsJSON, err := json.Marshal(c.Settings)
	if err != nil {
		return fmt.Errorf("chat: marshal settings: %w", err)
	}
	_, err = dbtx.Exec(ctx, `
		UPDATE conversations SET
			title = $2, photo_media_id = $3, description = $4,
			last_message_at = $5, last_message_seq = $6,
			last_message_snippet = $7, last_sender_id = $8,
			settings = $9, retention_days = $10, updated_at = $11
		WHERE id = $1`,
		c.ID, c.Title, c.PhotoMediaID, c.Description,
		c.LastMessageAt, c.LastMessageSeq, c.LastMessageSnippet, c.LastSenderID,
		settingsJSON, c.RetentionDays, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("chat: update conversation: %w", err)
	}
	return nil
}

// Tombstone soft-deletes a conversation whose last member left (kept for
// history, removed from chat lists).
func (r *ConversationRepo) Tombstone(ctx context.Context, dbtx tx.Tx, id int64, at time.Time) error {
	_, err := dbtx.Exec(ctx,
		`UPDATE conversations SET deleted_at = $2, updated_at = $2 WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("chat: tombstone conversation: %w", err)
	}
	return nil
}

// BumpLastMessage advances the denormalized last_message_* columns
// monotonically. The guard (`last_message_seq < seq`) makes out-of-order
// commits harmless: if a newer message's transaction commits first, a later
// older-sequence commit is a no-op, so the chat list never regresses and the
// snippet always matches the highest committed sequence.
func (r *ConversationRepo) BumpLastMessage(ctx context.Context, dbtx tx.Tx, id, seq int64, snippet *string, senderID *int64, at time.Time) (bool, error) {
	ct, err := dbtx.Exec(ctx, `
		UPDATE conversations
		SET last_message_at = $2, last_message_seq = $3,
		    last_message_snippet = $4, last_sender_id = $5, updated_at = $2
		WHERE id = $1 AND (last_message_seq IS NULL OR last_message_seq < $3)`,
		id, at, seq, snippet, senderID)
	if err != nil {
		return false, fmt.Errorf("chat: bump last message: %w", err)
	}
	return ct == 1, nil
}

// List returns the caller's chat list (API.md §7.1) with its per-user
// membership state, keyset-paginated on COALESCE(last_message_at, created_at)
// DESC, id DESC. For direct chats it also returns the counterpart's user id so
// titles can be derived from display names.
func (r *ConversationRepo) List(ctx context.Context, q domain.ConversationListQuery) ([]domain.ConversationRow, error) {
	query := `
		SELECT ` + conversationColumns + `,
		       m.role, m.last_read_seq, m.last_delivered_seq, m.last_read_at,
		       m.muted_until, m.pinned_at, m.archived_at, m.joined_at, m.left_at,
		       (SELECT m2.user_id FROM conversation_members m2
		         WHERE m2.conversation_id = c.id AND m2.user_id <> $1
		           AND m2.left_at IS NULL LIMIT 1) AS counterpart_id
		FROM conversations c
		JOIN conversation_members m ON m.conversation_id = c.id AND m.user_id = $1
		WHERE c.deleted_at IS NULL AND m.left_at IS NULL`

	var sb strings.Builder
	sb.WriteString(query)
	args := []any{q.UserID}

	switch q.Filter {
	case "pinned":
		sb.WriteString(` AND m.pinned_at IS NOT NULL`)
	case "archived":
		sb.WriteString(` AND m.archived_at IS NOT NULL`)
	case "groups":
		sb.WriteString(` AND c.type = 'group'`)
	case "direct":
		sb.WriteString(` AND c.type = 'direct'`)
	}
	if q.UnreadOnly {
		sb.WriteString(` AND COALESCE(c.last_message_seq, 0) > m.last_read_seq`)
	}
	if q.Cursor != nil {
		args = append(args, q.Cursor.Activity, q.Cursor.ID)
		sb.WriteString(` AND (COALESCE(c.last_message_at, c.created_at), c.id) < ($` +
			strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`)
	}
	args = append(args, q.Limit)
	sb.WriteString(` ORDER BY COALESCE(c.last_message_at, c.created_at) DESC, c.id DESC
		LIMIT $` + strconv.Itoa(len(args)))

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("chat: list conversations: %w", err)
	}
	defer rows.Close()

	out := make([]domain.ConversationRow, 0, q.Limit)
	for rows.Next() {
		row, err := scanConversationRow(rows, q.UserID)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanConversationRow maps one chat-list row (conversation + membership +
// counterpart id) to the domain row.
func scanConversationRow(row tx.Rows, userID int64) (*domain.ConversationRow, error) {
	var c domain.Conversation
	var m domain.Membership
	var settingsJSON []byte
	var counterpart *int64
	err := row.Scan(
		&c.ID, &c.Type, &c.Title, &c.PhotoMediaID, &c.Description, &c.CreatedBy,
		&c.LastMessageAt, &c.LastMessageSeq, &c.LastMessageSnippet, &c.LastSenderID,
		&settingsJSON, &c.RetentionDays, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
		&m.Role, &m.LastReadSeq, &m.LastDeliveredSeq, &m.LastReadAt, &m.MutedUntil,
		&m.PinnedAt, &m.ArchivedAt, &m.JoinedAt, &m.LeftAt,
		&counterpart,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrConversationNotFound
		}
		return nil, fmt.Errorf("chat: scan conversation row: %w", err)
	}
	m.ConversationID = c.ID
	m.UserID = userID
	settings, err := settingsFromJSON(settingsJSON)
	if err != nil {
		return nil, err
	}
	c.Settings = settings
	return &domain.ConversationRow{Conversation: c, Membership: m, CounterpartID: counterpart}, nil
}

// scanConversation maps a single conversation row to the aggregate.
func scanConversation(row tx.Row) (*domain.Conversation, error) {
	var c domain.Conversation
	var settingsJSON []byte
	err := row.Scan(
		&c.ID, &c.Type, &c.Title, &c.PhotoMediaID, &c.Description, &c.CreatedBy,
		&c.LastMessageAt, &c.LastMessageSeq, &c.LastMessageSnippet, &c.LastSenderID,
		&settingsJSON, &c.RetentionDays, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrConversationNotFound
		}
		return nil, fmt.Errorf("chat: scan conversation: %w", err)
	}
	settings, err := settingsFromJSON(settingsJSON)
	if err != nil {
		return nil, err
	}
	c.Settings = settings
	return &c, nil
}

// settingsFromJSON decodes the settings jsonb, defaulting when the column is
// null (direct conversations store no settings).
func settingsFromJSON(b []byte) (domain.Settings, error) {
	if len(b) == 0 {
		return domain.DefaultSettings(), nil
	}
	var s domain.Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return domain.Settings{}, fmt.Errorf("chat: decode settings: %w", err)
	}
	if s.HistoryVisible == "" {
		s.HistoryVisible = domain.HistoryVisibleAll
	}
	return s, nil
}
