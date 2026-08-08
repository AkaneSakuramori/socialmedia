package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// MarkRead advances the caller's read/delivered cursors monotonically
// (API.md §10.1/§10.3, shared with §7.12). GREATEST max-merge means
// concurrent receipts from two devices can never regress the cursor; the
// receipt.read outbox delta fires only for newly-read messages, addressed to
// the senders of those messages.
func (s *service) MarkRead(ctx context.Context, cmd MarkReadCommand) (*ReceiptResult, error) {
	if cmd.ReadSeq <= 0 {
		return nil, validationError("last_read_seq", "must_be_positive")
	}
	delivered := int64(0)
	if cmd.DeliveredSeq != nil {
		delivered = *cmd.DeliveredSeq
		if delivered <= 0 {
			return nil, validationError("deliver_up_to_seq", "must_be_positive")
		}
	}

	mem, err := s.deps.Memberships.FindActive(ctx, cmd.ConversationID, cmd.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}
	beforeRead, beforeDelivered := mem.LastReadSeq, mem.LastDeliveredSeq

	now := s.now()
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin mark-read transaction")
	}
	defer dbtx.Rollback(ctx)

	advRead, advDelivered, err := s.deps.Memberships.MarkRead(ctx, dbtx, cmd.ConversationID, cmd.UserID, cmd.ReadSeq, delivered, now)
	if err != nil {
		return nil, makeErr(err, "chat: mark read")
	}

	// The data model forbids a read cursor ahead of the delivered cursor
	// (migration 000006 CHECK), so the effective delivered cursor is at least
	// the read cursor — a read receipt implies delivery.
	effRead := max64(beforeRead, cmd.ReadSeq)
	effDelivered := max64(beforeDelivered, max64(delivered, cmd.ReadSeq))

	if advRead && effRead > beforeRead {
		senders, err := s.deps.Messages.SenderIDsBetween(ctx, dbtx, cmd.ConversationID, beforeRead, effRead)
		if err != nil {
			return nil, err
		}
		if len(senders) > 0 {
			payload, err := json.Marshal(map[string]any{
				"conversation_id": cmd.ConversationID,
				"user_id":         cmd.UserID,
				"last_read_seq":   effRead,
				"at":              now,
			})
			if err != nil {
				return nil, err
			}
			if err := s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{{
				EventType:       domain.EventReceiptRead,
				ConversationID:  &cmd.ConversationID,
				EntityID:        &cmd.UserID,
				ActorUserID:     &cmd.UserID,
				AffectedUserIDs: senders,
				Payload:         payload,
			}}); err != nil {
				return nil, err
			}
		}
	}

	if advDelivered && effDelivered > beforeDelivered {
		payload, err := json.Marshal(map[string]any{
			"conversation_id":    cmd.ConversationID,
			"user_id":            cmd.UserID,
			"last_delivered_seq": effDelivered,
		})
		if err != nil {
			return nil, err
		}
		if err := s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{{
			EventType:       domain.EventReceiptDelivered,
			ConversationID:  &cmd.ConversationID,
			EntityID:        &cmd.UserID,
			ActorUserID:     &cmd.UserID,
			AffectedUserIDs: []int64{cmd.UserID},
			Payload:         payload,
		}}); err != nil {
			return nil, err
		}
	}

	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit mark-read transaction")
	}

	return &ReceiptResult{
		LastReadSeq:      strconv.FormatInt(effRead, 10),
		LastDeliveredSeq: strconv.FormatInt(effDelivered, 10),
	}, nil
}

// GetReceipts returns the per-member read state (API.md §10.2).
func (s *service) GetReceipts(ctx context.Context, cmd GetReceiptsCommand) (*ReceiptsResult, error) {
	if _, err := s.deps.Memberships.FindActive(ctx, cmd.ConversationID, cmd.UserID); err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}
	conv, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}

	receipts, err := s.deps.Memberships.ListReceipts(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(receipts))
	for _, r := range receipts {
		ids = append(ids, r.UserID)
	}
	names := map[int64]string{}
	if len(ids) > 0 {
		users, err := s.deps.Users.ListByIDs(ctx, s.deps.DB, ids)
		if err != nil {
			return nil, err
		}
		names = displayNames(users)
	}

	readers := make([]ReaderView, 0, len(receipts))
	for _, r := range receipts {
		readers = append(readers, ReaderView{
			UserID:      strconv.FormatInt(r.UserID, 10),
			DisplayName: names[r.UserID],
			LastReadSeq: strconv.FormatInt(r.LastReadSeq, 10),
			LastReadAt:  r.LastReadAt,
		})
	}

	return &ReceiptsResult{
		ConversationID: strconv.FormatInt(cmd.ConversationID, 10),
		LastMessageSeq: seqString(conv.LastMessageSeq),
		Readers:        readers,
	}, nil
}

// max64 returns the larger of two int64s (Go lacks a generic builtin here).
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
