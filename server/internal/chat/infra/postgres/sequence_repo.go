package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// SequenceRepo persists the durable per-conversation sequence counter
// (DATABASE.md §5.4, conversation_sequences). The Redis hot path lands with
// the messaging milestone; M1 only guarantees the recovery row exists.
type SequenceRepo struct{}

// NewSequenceRepo builds the sequence repository.
func NewSequenceRepo() *SequenceRepo { return &SequenceRepo{} }

// Init creates the counter row for a new conversation within the tx.
func (r *SequenceRepo) Init(ctx context.Context, dbtx tx.Tx, conversationID int64) error {
	_, err := dbtx.Exec(ctx, `
		INSERT INTO conversation_sequences (conversation_id, last_sequence, updated_at)
		VALUES ($1, 0, $2)
		ON CONFLICT (conversation_id) DO NOTHING`,
		conversationID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("chat: init sequence: %w", err)
	}
	return nil
}
