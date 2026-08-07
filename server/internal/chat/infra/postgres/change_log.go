package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChangeLogRepo persists the transactional outbox / sync feed (DATABASE.md
// §7.1, change_log). Entries are written in the same transaction as the domain
// write (outbox pattern, ARCHITECTURE.md §37.4); consumers read them in global
// order.
type ChangeLogRepo struct {
	pool *pgxpool.Pool
}

// NewChangeLogRepo builds the repository over the shared pool.
func NewChangeLogRepo(pool *pgxpool.Pool) *ChangeLogRepo {
	return &ChangeLogRepo{pool: pool}
}

// Append inserts outbox entries within the given transaction.
func (r *ChangeLogRepo) Append(ctx context.Context, dbtx tx.Tx, entries []domain.ChangeLogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	values := make([]string, 0, len(entries))
	args := make([]any, 0, len(entries)*6)
	for i, e := range entries {
		base := i * 6
		values = append(values,
			`($`+itoa(base+1)+`,$`+itoa(base+2)+`,$`+itoa(base+3)+`,$`+itoa(base+4)+`,$`+itoa(base+5)+`,$`+itoa(base+6)+`)`)
		args = append(args, e.EventType, e.ConversationID, e.EntityID,
			e.ActorUserID, e.AffectedUserIDs, e.Payload)
	}
	_, err := dbtx.Exec(ctx, `
		INSERT INTO change_log
			(event_type, conversation_id, entity_id, actor_user_id, affected_user_ids, payload)
		VALUES `+strings.Join(values, ","), args...)
	if err != nil {
		return fmt.Errorf("chat: append change_log: %w", err)
	}
	return nil
}
