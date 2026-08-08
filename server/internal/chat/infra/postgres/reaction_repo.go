package postgres

import (
	"context"
	"fmt"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReactionRepo is the pgx-backed domain.ReactionRepository (DATABASE.md §5.6).
// Counts are derived by GROUP BY, never stored.
type ReactionRepo struct {
	pool *pgxpool.Pool
}

// NewReactionRepo builds the repository over the shared pool.
func NewReactionRepo(pool *pgxpool.Pool) *ReactionRepo {
	return &ReactionRepo{pool: pool}
}

// Add inserts a reaction within the given transaction; a duplicate
// (message,user,emoji) is a no-op (added=false, the §8.6 "toggle again = no-op
// 200").
func (r *ReactionRepo) Add(ctx context.Context, dbtx tx.Tx, rec *domain.ReactionRow) (added bool, err error) {
	ct, err := dbtx.Exec(ctx, `
		INSERT INTO message_reactions (id, message_id, user_id, emoji, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (message_id, user_id, emoji) DO NOTHING`,
		rec.ID, rec.MessageID, rec.UserID, rec.Emoji, rec.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("chat: add reaction: %w", err)
	}
	return ct == 1, nil
}

// Remove deletes the caller's reaction within the given transaction; returns
// whether a row was actually removed (false = nothing to remove).
func (r *ReactionRepo) Remove(ctx context.Context, dbtx tx.Tx, messageID, userID int64, emoji string) (bool, error) {
	ct, err := dbtx.Exec(ctx, `
		DELETE FROM message_reactions
		WHERE message_id = $1 AND user_id = $2 AND emoji = $3`,
		messageID, userID, emoji)
	if err != nil {
		return false, fmt.Errorf("chat: remove reaction: %w", err)
	}
	return ct == 1, nil
}

// DistinctEmoji returns how many distinct emoji a message already has
// (API.md §8.6: max 20).
func (r *ReactionRepo) DistinctEmoji(ctx context.Context, messageID int64) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT count(DISTINCT emoji) FROM message_reactions WHERE message_id = $1`,
		messageID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("chat: distinct emoji: %w", err)
	}
	return n, nil
}

// Count returns the reactor count for a message + emoji.
func (r *ReactionRepo) Count(ctx context.Context, messageID int64, emoji string) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM message_reactions WHERE message_id = $1 AND emoji = $2`,
		messageID, emoji).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("chat: count reaction: %w", err)
	}
	return n, nil
}

// CountsByMessages aggregates emoji counts for a page of messages in one query
// (message_id -> emoji -> count).
func (r *ReactionRepo) CountsByMessages(ctx context.Context, messageIDs []int64) (map[int64]map[string]int64, error) {
	if len(messageIDs) == 0 {
		return map[int64]map[string]int64{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT message_id, emoji, count(*) AS n
		FROM message_reactions
		WHERE message_id = ANY($1)
		GROUP BY message_id, emoji`, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("chat: reaction counts: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]map[string]int64, len(messageIDs))
	for rows.Next() {
		var mid, n int64
		var emoji string
		if err := rows.Scan(&mid, &emoji, &n); err != nil {
			return nil, fmt.Errorf("chat: scan reaction count: %w", err)
		}
		if out[mid] == nil {
			out[mid] = map[string]int64{}
		}
		out[mid][emoji] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UserIDsByMessages aggregates the reactor user ids per (message, emoji) for
// the rendered reaction chips (message_id -> emoji -> user ids), in one query.
func (r *ReactionRepo) UserIDsByMessages(ctx context.Context, messageIDs []int64) (map[int64]map[string][]int64, error) {
	if len(messageIDs) == 0 {
		return map[int64]map[string][]int64{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT message_id, emoji, user_id
		FROM message_reactions
		WHERE message_id = ANY($1)
		ORDER BY message_id, emoji, created_at`, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("chat: reaction user ids: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]map[string][]int64, len(messageIDs))
	for rows.Next() {
		var mid, uid int64
		var emoji string
		if err := rows.Scan(&mid, &emoji, &uid); err != nil {
			return nil, fmt.Errorf("chat: scan reaction user: %w", err)
		}
		if out[mid] == nil {
			out[mid] = map[string][]int64{}
		}
		out[mid][emoji] = append(out[mid][emoji], uid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Reactors lists the reactors (user_id + at) for a message + emoji
// (API.md §8.8), most recent first. Display names are resolved by the caller.
func (r *ReactionRepo) Reactors(ctx context.Context, messageID int64, emoji string) ([]domain.Reactor, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, created_at FROM message_reactions
		WHERE message_id = $1 AND emoji = $2
		ORDER BY created_at DESC, user_id DESC`, messageID, emoji)
	if err != nil {
		return nil, fmt.Errorf("chat: list reactors: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Reactor, 0, 8)
	for rows.Next() {
		var re domain.Reactor
		if err := rows.Scan(&re.UserID, &re.At); err != nil {
			return nil, fmt.Errorf("chat: scan reactor: %w", err)
		}
		out = append(out, re)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
