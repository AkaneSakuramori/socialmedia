package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MessageRepo is the pgx-backed domain.MessageRepository (DATABASE.md §5.3).
type MessageRepo struct {
	pool *pgxpool.Pool
}

// NewMessageRepo builds the repository over the shared pool.
func NewMessageRepo(pool *pgxpool.Pool) *MessageRepo {
	return &MessageRepo{pool: pool}
}

// messageColumns is the SELECT projection shared by every message lookup.
const messageColumns = `
	id, conversation_id, sequence, client_msg_id, sender_id, type, content,
	attachment_envelope, mentions, reply_to_id, edit_count, edited_at,
	deleted_at, deleted_by, global_seq, created_at`

// Insert writes a message within the given transaction. The partial unique
// index (sender_id, client_msg_id) collapses a retried send (ON CONFLICT DO
// NOTHING); inserted=false tells the caller to re-select the original row for
// the idempotent replay (API.md §8.2). A composite-PK collision surfaces as
// ErrSequenceConflict (DATABASE.md §11: the PK is the final guard against
// sequence reuse; the caller retries with the next sequence).
func (r *MessageRepo) Insert(ctx context.Context, dbtx tx.Tx, m *domain.Message) (inserted bool, err error) {
	var insertedID int64
	err = dbtx.QueryRow(ctx, `
		INSERT INTO messages (
			conversation_id, sequence, id, client_msg_id, sender_id, type, content,
			attachment_envelope, mentions, reply_to_id, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (sender_id, client_msg_id)
			WHERE client_msg_id IS NOT NULL AND sender_id IS NOT NULL
			DO NOTHING
		RETURNING id, global_seq`,
		m.ConversationID, m.Sequence, m.ID, m.ClientMsgID, m.SenderID, m.Type,
		m.Content, m.AttachmentEnvelope, m.Mentions, m.ReplyToID, m.CreatedAt).
		Scan(&insertedID, &m.GlobalSeq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The partial unique index collapsed the retry: idempotent replay.
			return false, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "messages_pkey" {
			return false, domain.ErrSequenceConflict
		}
		return false, fmt.Errorf("chat: insert message: %w", err)
	}
	return true, nil
}

// FindByClientMsgID loads the original message for an idempotent replay.
func (r *MessageRepo) FindByClientMsgID(ctx context.Context, q tx.Querier, senderID int64, clientMsgID string) (*domain.Message, error) {
	return scanMessage(q.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM messages
		 WHERE sender_id = $1 AND client_msg_id = $2`,
		senderID, clientMsgID))
}

// FindByID loads a message by its snowflake id, or ErrMessageNotFound.
func (r *MessageRepo) FindByID(ctx context.Context, id int64) (*domain.Message, error) {
	return scanMessage(r.pool.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE id = $1`, id))
}

// FindByConversationSeq resolves a reply_to_seq reference, or
// ErrMessageNotFound.
func (r *MessageRepo) FindByConversationSeq(ctx context.Context, conversationID, seq int64) (*domain.Message, error) {
	return scanMessage(r.pool.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM messages
		 WHERE conversation_id = $1 AND sequence = $2`,
		conversationID, seq))
}

// ListByConversation returns a page of messages (API.md §8.1). With
// AfterGlobalSeq the page is the delta-sync poll (global_seq > cursor, global
// order ascending); otherwise it is the history keyset seek (sequence < before,
// newest-first; the caller reverses for the ascending wire order). Limit must
// be limit+1 so the caller can detect has_more.
func (r *MessageRepo) ListByConversation(ctx context.Context, q domain.MessageListQuery) ([]domain.Message, error) {
	var query string
	var args []any
	if q.AfterGlobalSeq != nil {
		query = `
			SELECT ` + messageColumns + ` FROM messages
			WHERE conversation_id = $1 AND global_seq > $2
			ORDER BY global_seq ASC LIMIT $3`
		args = []any{q.ConversationID, *q.AfterGlobalSeq, q.Limit}
	} else {
		query = `
			SELECT ` + messageColumns + ` FROM messages
			WHERE conversation_id = $1`
		args = []any{q.ConversationID}
		if q.BeforeSeq != nil {
			args = append(args, *q.BeforeSeq)
			query += ` AND sequence < $` + itoa(len(args))
		}
		args = append(args, q.Limit)
		query += ` ORDER BY sequence DESC LIMIT $` + itoa(len(args))
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("chat: list messages: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Message, 0, q.Limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Edit atomically records an edit: guarded UPDATE of content/edit_count/
// edited_at (WHERE deleted_at IS NULL) plus an append-only message_edits row,
// in the given transaction. Returns false when the message is gone or already
// tombstoned — the edit cannot apply to a tombstone.
func (r *MessageRepo) Edit(ctx context.Context, dbtx tx.Tx, editID int64, m *domain.Message, oldContent string, at time.Time) (bool, error) {
	ct, err := dbtx.Exec(ctx, `
		UPDATE messages
		SET content = $2, edit_count = edit_count + 1, edited_at = $3
		WHERE id = $1 AND deleted_at IS NULL`,
		m.ID, m.Content, at)
	if err != nil {
		return false, fmt.Errorf("chat: apply edit: %w", err)
	}
	if ct == 0 {
		return false, nil
	}
	_, err = dbtx.Exec(ctx, `
		INSERT INTO message_edits (id, message_id, edited_by, old_content, edited_at)
		VALUES ($1,$2,$3,$4,$5)`,
		editID, m.ID, m.SenderIDOrZero(), oldContent, at)
	if err != nil {
		return false, fmt.Errorf("chat: record edit history: %w", err)
	}
	return true, nil
}

// Tombstone soft-deletes a message (API.md §8.5 mode=all): guarded UPDATE
// WHERE deleted_at IS NULL. Content is retained in the row for audit
// (DATABASE.md §5.3) and rendered as "deleted" at read time. Returns false
// when already tombstoned or gone.
func (r *MessageRepo) Tombstone(ctx context.Context, dbtx tx.Tx, id, deletedBy int64, at time.Time) (bool, error) {
	ct, err := dbtx.Exec(ctx, `
		UPDATE messages
		SET deleted_at = $2, deleted_by = $3
		WHERE id = $1 AND deleted_at IS NULL`,
		id, at, deletedBy)
	if err != nil {
		return false, fmt.Errorf("chat: tombstone message: %w", err)
	}
	return ct == 1, nil
}

// SenderIDsBetween returns the distinct senders of messages with
// from < sequence <= to in a conversation — the receipt.read fan-out list for
// the outbox (only the delta of newly-read messages).
func (r *MessageRepo) SenderIDsBetween(ctx context.Context, dbtx tx.Tx, conversationID, from, to int64) ([]int64, error) {
	rows, err := dbtx.Query(ctx, `
		SELECT DISTINCT sender_id FROM messages
		WHERE conversation_id = $1 AND sequence > $2 AND sequence <= $3
		  AND sender_id IS NOT NULL`,
		conversationID, from, to)
	if err != nil {
		return nil, fmt.Errorf("chat: senders between: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("chat: scan sender: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanMessage maps one message row to the domain aggregate.
func scanMessage(row tx.Row) (*domain.Message, error) {
	var m domain.Message
	err := row.Scan(
		&m.ID, &m.ConversationID, &m.Sequence, &m.ClientMsgID, &m.SenderID,
		&m.Type, &m.Content, &m.AttachmentEnvelope, &m.Mentions, &m.ReplyToID,
		&m.EditCount, &m.EditedAt, &m.DeletedAt, &m.DeletedBy, &m.GlobalSeq,
		&m.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMessageNotFound
		}
		return nil, fmt.Errorf("chat: scan message: %w", err)
	}
	return &m, nil
}
