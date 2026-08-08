package postgres

import (
	"context"
	"fmt"

	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// SequenceSource allocates per-conversation message sequences
// (ARCHITECTURE.md §13.2, DATABASE.md §5.4). The Redis counter is the hot path
// (atomic INCR, correct across API instances); conversation_sequences is the
// durable recovery floor. The PG composite PK (conversation_id, sequence) is
// the final guard against reuse — if a sequence ever collides, the insert
// surfaces ErrSequenceConflict and the caller retries.
//
// Cold-start safety: when the Redis key is absent (Redis restart/failover) the
// counter is bootstrapped to the durable floor with SET NX before INCR, so a
// sequence already persisted can never be reissued. On Redis loss the allocator
// falls back to a single-row PG increment (serialized per conversation, the
// spec-sanctioned fallback).
type SequenceSource struct {
	pool  *pgxpool.Pool
	redis *redis.Client
}

// NewSequenceSource builds the sequence allocator over PG + Redis.
func NewSequenceSource(pool *pgxpool.Pool, redis *redis.Client) *SequenceSource {
	return &SequenceSource{pool: pool, redis: redis}
}

// seqKey is the per-conversation Redis counter key.
func seqKey(conversationID int64) string {
	return fmt.Sprintf("conv:%d:seq", conversationID)
}

// Next allocates the next sequence for a conversation.
func (s *SequenceSource) Next(ctx context.Context, conversationID int64) (int64, error) {
	key := seqKey(conversationID)

	exists, err := s.redis.Exists(ctx, key).Result()
	if err != nil {
		return s.nextFromPG(ctx, conversationID)
	}
	if exists == 0 {
		// Cold counter: bootstrap to the durable floor so we never reissue a
		// persisted sequence. SET NX makes a concurrent bootstrap safe (the
		// loser's INCR continues from the winner's value).
		floor, ferr := s.Floor(ctx, conversationID)
		if ferr == nil && floor > 0 {
			_ = s.redis.SetNX(ctx, key, floor, 0).Err()
		}
	}

	seq, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return s.nextFromPG(ctx, conversationID)
	}
	return seq, nil
}

// nextFromPG is the fallback allocator: a single-row increment serialized per
// conversation (used only when Redis is unavailable).
func (s *SequenceSource) nextFromPG(ctx context.Context, conversationID int64) (int64, error) {
	var seq int64
	err := s.pool.QueryRow(ctx, `
		UPDATE conversation_sequences
		SET last_sequence = last_sequence + 1, updated_at = now()
		WHERE conversation_id = $1
		RETURNING last_sequence`, conversationID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("chat: allocate sequence from postgres: %w", err)
	}
	return seq, nil
}

// Persist reconciles the durable floor inside the send transaction via
// GREATEST max-merge (idempotent — DATABASE.md §5.4): the floor can only rise,
// so a concurrent flush or a later send can never regress it.
func (s *SequenceSource) Persist(ctx context.Context, dbtx tx.Tx, conversationID, sequence int64) error {
	_, err := dbtx.Exec(ctx, `
		UPDATE conversation_sequences
		SET last_sequence = GREATEST(last_sequence, $2), updated_at = now()
		WHERE conversation_id = $1`,
		conversationID, sequence)
	if err != nil {
		return fmt.Errorf("chat: persist sequence floor: %w", err)
	}
	return nil
}

// Floor returns the durable counter (0 when no row exists yet).
func (s *SequenceSource) Floor(ctx context.Context, conversationID int64) (int64, error) {
	var floor int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(last_sequence, 0) FROM conversation_sequences
		WHERE conversation_id = $1`, conversationID).Scan(&floor)
	if err != nil {
		return 0, fmt.Errorf("chat: read sequence floor: %w", err)
	}
	return floor, nil
}
