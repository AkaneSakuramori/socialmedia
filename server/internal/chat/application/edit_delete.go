package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// EditMessage edits a message within the sender-only edit window (API.md §8.4).
// Edits are append-only (message_edits) and content-only; concurrent edits both
// record and last-write-wins on the visible body. The guarded UPDATE (WHERE
// deleted_at IS NULL) rejects an edit racing a delete-for-all.
func (s *service) EditMessage(ctx context.Context, cmd EditMessageCommand) (*MessageView, error) {
	m, err := s.deps.Messages.FindByID(ctx, cmd.MessageID)
	if err != nil {
		return nil, err
	}
	if !m.SenderOf(cmd.UserID) {
		return nil, domain.ErrNotSender
	}
	if m.Deleted() {
		return nil, domain.ErrMessageDeleted
	}
	if !m.Editable(s.now()) {
		return nil, domain.ErrEditWindowExpired
	}
	text := strings.TrimSpace(cmd.NewText)
	if text == "" {
		return nil, validationError("content.text", "required")
	}
	if len([]rune(text)) > domain.MaxMessageTextLen {
		return nil, validationError("content.text", "too_long")
	}
	if m.Content == nil {
		return nil, domain.ErrMessageNotEditable // media/system messages cannot be edited
	}
	oldContent := *m.Content

	editID, err := s.deps.IDs.NextID()
	if err != nil {
		return nil, makeErr(err, "chat: allocate edit id")
	}
	now := s.now()
	m.Content = &text // the repo's guarded UPDATE writes this

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin edit transaction")
	}
	defer dbtx.Rollback(ctx)

	updated, err := s.deps.Messages.Edit(ctx, dbtx, editID, m, oldContent, now)
	if err != nil {
		return nil, makeErr(err, "chat: apply edit")
	}
	if !updated {
		return nil, domain.ErrMessageNotFound // deleted or purged concurrently
	}
	if err := s.appendMessageEdited(ctx, dbtx, *m); err != nil {
		return nil, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit edit transaction")
	}

	m.EditCount++
	m.EditedAt = &now
	return s.viewFor(ctx, *m), nil
}

// DeleteMessage deletes a message (API.md §8.5). mode=all tombstones the row
// (the sequence slot is never re-used, so pagination/sync stay consistent);
// mode=self is client-local in v1 and a server no-op (DATABASE.md §5.7).
func (s *service) DeleteMessage(ctx context.Context, cmd DeleteMessageCommand) (*DeleteMessageResult, error) {
	if cmd.Mode == "" {
		cmd.Mode = "all"
	}
	if cmd.Mode != "all" && cmd.Mode != "self" {
		return nil, validationError("mode", "invalid_mode")
	}
	idStr := strconv.FormatInt(cmd.MessageID, 10)
	if cmd.Mode == "self" {
		return &DeleteMessageResult{Deleted: "self", MessageID: idStr}, nil
	}

	m, err := s.deps.Messages.FindByID(ctx, cmd.MessageID)
	if err != nil {
		return nil, err
	}
	mem, err := s.deps.Memberships.FindActive(ctx, m.ConversationID, cmd.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}
	if !m.SenderOf(cmd.UserID) && !mem.Role.AtLeast(domain.RoleAdmin) {
		return nil, domain.ErrNotSender
	}
	if m.Deleted() {
		return &DeleteMessageResult{Deleted: "all", MessageID: idStr}, nil // idempotent 200
	}

	now := s.now()
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin delete transaction")
	}
	defer dbtx.Rollback(ctx)

	tombstoned, err := s.deps.Messages.Tombstone(ctx, dbtx, m.ID, cmd.UserID, now)
	if err != nil {
		return nil, makeErr(err, "chat: tombstone message")
	}
	if !tombstoned {
		return &DeleteMessageResult{Deleted: "all", MessageID: idStr}, nil // concurrent delete
	}
	if err := s.appendMessageDeleted(ctx, dbtx, *m, cmd.UserID, now); err != nil {
		return nil, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit delete transaction")
	}

	return &DeleteMessageResult{Deleted: "all", MessageID: idStr}, nil
}

// appendMessageEdited writes the message.edited outbox row.
func (s *service) appendMessageEdited(ctx context.Context, dbtx tx.Tx, m domain.Message) error {
	members, err := s.deps.Memberships.ActiveUserIDs(ctx, dbtx, m.ConversationID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"message_id":      m.ID,
		"conversation_id": m.ConversationID,
		"sequence":        m.Sequence,
		"content":         map[string]any{"text": m.Content},
		"edited_at":       m.EditedAt,
		"global_seq":      m.GlobalSeq,
	})
	if err != nil {
		return err
	}
	return s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{{
		EventType:       domain.EventMessageEdited,
		ConversationID:  &m.ConversationID,
		EntityID:        &m.ID,
		ActorUserID:     m.SenderID,
		AffectedUserIDs: members,
		Payload:         payload,
	}})
}

// appendMessageDeleted writes the message.deleted outbox row with the §18.7
// tombstone data.
func (s *service) appendMessageDeleted(ctx context.Context, dbtx tx.Tx, m domain.Message, deletedBy int64, at time.Time) error {
	members, err := s.deps.Memberships.ActiveUserIDs(ctx, dbtx, m.ConversationID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"message_id":      m.ID,
		"conversation_id": m.ConversationID,
		"sequence":        m.Sequence,
		"mode":            "all",
		"deleted_by":      deletedBy,
		"deleted_at":      at,
		"global_seq":      m.GlobalSeq,
	})
	if err != nil {
		return err
	}
	return s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{{
		EventType:       domain.EventMessageDeleted,
		ConversationID:  &m.ConversationID,
		EntityID:        &m.ID,
		ActorUserID:     m.SenderID,
		AffectedUserIDs: members,
		Payload:         payload,
	}})
}
