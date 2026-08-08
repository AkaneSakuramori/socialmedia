package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// maxSendRetries bounds the ErrSequenceConflict retry loop (DATABASE.md §11:
// the composite PK is the final guard; a collision is re-allocated and
// retried, never serialized on a row lock).
const maxSendRetries = 3

// SendMessage persists a message atomically (API.md §8.2): sequence
// allocation, the message row, the monotonic conversation bump, and the
// change_log outbox commit in one transaction. It is the durable path behind
// both POST /messages and the WS message.send frame.
//
// Exactly-once intent: the partial unique index (sender_id, client_msg_id)
// collapses a retried send even if the HTTP idempotency cache is lost; the
// original row is re-selected and returned (Created=false → HTTP 200).
func (s *service) SendMessage(ctx context.Context, cmd SendMessageCommand) (*SendMessageResult, error) {
	mtype, err := s.validateSendCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}

	// Gate: the caller must be an active member of the conversation.
	if _, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID); err != nil {
		return nil, err
	}
	if _, err := s.deps.Memberships.FindActive(ctx, cmd.ConversationID, cmd.UserID); err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}

	var replyToID *int64
	if cmd.ReplyToSeq != nil {
		reply, err := s.deps.Messages.FindByConversationSeq(ctx, cmd.ConversationID, *cmd.ReplyToSeq)
		if err != nil {
			if errors.Is(err, domain.ErrMessageNotFound) {
				return nil, validationError("reply_to_seq", "invalid_reply_target")
			}
			return nil, err
		}
		replyToID = &reply.ID
	}

	for attempt := 0; attempt < maxSendRetries; attempt++ {
		view, created, err := s.sendOnce(ctx, cmd, mtype, replyToID)
		if err == nil {
			return &SendMessageResult{View: *view, Created: created}, nil
		}
		if !errors.Is(err, domain.ErrSequenceConflict) {
			return nil, err
		}
		// A sequence collision against the composite PK: re-allocate and retry.
	}

	return nil, domain.ErrSequenceConflict
}

// sendOnce runs one allocation+transaction attempt of the send path.
func (s *service) sendOnce(ctx context.Context, cmd SendMessageCommand, mtype domain.MessageType, replyToID *int64) (*MessageView, bool, error) {
	seq, err := s.deps.SequenceSource.Next(ctx, cmd.ConversationID)
	if err != nil {
		return nil, false, err
	}

	msgID, err := s.deps.IDs.NextID()
	if err != nil {
		return nil, false, makeErr(err, "chat: allocate message id")
	}

	now := s.now()
	msg := &domain.Message{
		ID:                 msgID,
		ConversationID:     cmd.ConversationID,
		Sequence:           seq,
		ClientMsgID:        &cmd.ClientMsgID,
		SenderID:           &cmd.UserID,
		Type:               mtype,
		Content:            cmd.Text,
		AttachmentEnvelope: encodeEnvelope(cmd.Media),
		Mentions:           cmd.Mentions,
		ReplyToID:          replyToID,
		CreatedAt:          now,
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, false, makeErr(err, "chat: begin send transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	inserted, err := s.deps.Messages.Insert(ctx, dbtx, msg)
	if err != nil {
		if errors.Is(err, domain.ErrSequenceConflict) {
			return nil, false, err // caller retries with the next sequence
		}
		return nil, false, makeErr(err, "chat: insert message")
	}

	if !inserted {
		// Idempotent replay: the partial unique index collapsed the duplicate.
		// The winner may not have committed yet, so poll briefly — inside the
		// same transaction, so no second pool connection is taken while this
		// write tx is open.
		orig, err := s.replayOriginal(ctx, dbtx, cmd.UserID, cmd.ClientMsgID)
		if err != nil {
			return nil, false, err
		}
		if err := dbtx.Rollback(ctx); err != nil {
			return nil, false, makeErr(err, "chat: rollback send transaction")
		}
		return s.viewFor(ctx, *orig), false, nil
	}

	// Persist the durable floor (GREATEST max-merge), bump the conversation
	// monotonically, and append the outbox row — all atomic with the message.
	if err := s.deps.SequenceSource.Persist(ctx, dbtx, cmd.ConversationID, seq); err != nil {
		return nil, false, makeErr(err, "chat: persist sequence floor")
	}
	if _, err := s.deps.Conversations.BumpLastMessage(ctx, dbtx, cmd.ConversationID, seq, s.snippet(*msg), &cmd.UserID, now); err != nil {
		return nil, false, makeErr(err, "chat: bump conversation last message")
	}
	if err := s.appendMessageCreated(ctx, dbtx, cmd, *msg); err != nil {
		return nil, false, err
	}

	if err := dbtx.Commit(ctx); err != nil {
		return nil, false, makeErr(err, "chat: commit send transaction")
	}

	return s.viewFor(ctx, *msg), true, nil
}

// replayOriginal re-selects the original message for an idempotent replay.
// The winning transaction may commit a few ms after the losing insert's
// conflict, so a bounded poll replaces a spurious error. The querier is the
// caller's still-open transaction (see Deps.DB).
func (s *service) replayOriginal(ctx context.Context, q tx.Querier, senderID int64, clientMsgID string) (*domain.Message, error) {
	const attempts = 10
	for i := 0; i < attempts; i++ {
		orig, err := s.deps.Messages.FindByClientMsgID(ctx, q, senderID, clientMsgID)
		if err == nil {
			return orig, nil
		}
		if !errors.Is(err, domain.ErrMessageNotFound) {
			return nil, err
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, domain.ErrClientMsgIDConflict
}

// viewFor builds the §8.1 view for a single message (send/edit/get results)
// with the page enrichment for one row.
func (s *service) viewFor(ctx context.Context, msg domain.Message) *MessageView {
	e, err := s.enrichPage(ctx, []domain.Message{msg})
	if err != nil {
		// The write already committed; degrade to an un-enriched view rather
		// than failing the caller for a derived-field lookup.
		e = emptyEnrichment()
	}
	v := s.messageView(msg, e)
	return &v
}

// emptyEnrichment is the no-data fallback used when derived lookups fail.
func emptyEnrichment() *messagePageEnrichment {
	return &messagePageEnrichment{
		senders:        map[int64]string{},
		replies:        map[int64]domain.ReplyTo{},
		reactionCounts: map[int64]map[string]int64{},
		reactionUsers:  map[int64]map[string][]int64{},
		cursors:        map[int64]domain.CursorRow{},
		readAt:         map[int64]*time.Time{},
	}
}

// validateSendCommand checks the §8.2 wire rules and returns the message type.
func (s *service) validateSendCommand(ctx context.Context, cmd SendMessageCommand) (domain.MessageType, error) {
	if strings.TrimSpace(cmd.ClientMsgID) == "" {
		return "", validationError("client_msg_id", "required")
	}
	if len(cmd.ClientMsgID) > 255 {
		return "", validationError("client_msg_id", "too_long")
	}

	mtype, err := domain.ParseMessageType(cmd.Type)
	if err != nil {
		return "", validationError("type", "invalid_type")
	}
	if mtype != domain.MessageTypeText && mtype != domain.MessageTypeMedia {
		return "", validationError("type", "invalid_type")
	}

	hasText := cmd.Text != nil && strings.TrimSpace(*cmd.Text) != ""
	hasMedia := len(cmd.Media) > 0
	switch {
	case hasText && hasMedia:
		return "", validationError("content", "exactly_one_of_content_or_media")
	case !hasText && !hasMedia:
		return "", validationError("content", "exactly_one_of_content_or_media")
	case hasText:
		if len([]rune(*cmd.Text)) > domain.MaxMessageTextLen {
			return "", validationError("content.text", "too_long")
		}
	case hasMedia:
		if len(cmd.Media) > domain.MaxMediaPerMessage {
			return "", validationError("media", "too_many")
		}
		for _, a := range cmd.Media {
			if strings.TrimSpace(a.MediaID) == "" {
				return "", validationError("media", "media_id_required")
			}
		}
	}

	if len(cmd.Mentions) > 0 {
		if err := s.validateMentions(ctx, cmd.ConversationID, cmd.Mentions); err != nil {
			return "", err
		}
	}
	return mtype, nil
}

// validateMentions verifies every mention is an active conversation member.
func (s *service) validateMentions(ctx context.Context, conversationID int64, mentions []int64) error {
	ids, err := s.deps.Memberships.ActiveUserIDs(ctx, s.deps.DB, conversationID)
	if err != nil {
		return err
	}
	member := make(map[int64]bool, len(ids))
	for _, id := range ids {
		member[id] = true
	}
	seen := map[int64]bool{}
	for _, id := range mentions {
		if id <= 0 {
			return validationError("mentions", "invalid_user_id")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if !member[id] {
			return domain.ErrMentionNotMember
		}
	}
	return nil
}

// appendMessageCreated writes the message.created outbox row — a self-contained
// payload so sync/realtime consumers never re-query domain tables
// (DATABASE.md §7.1).
func (s *service) appendMessageCreated(ctx context.Context, dbtx tx.Tx, cmd SendMessageCommand, m domain.Message) error {
	members, err := s.deps.Memberships.ActiveUserIDs(ctx, dbtx, cmd.ConversationID)
	if err != nil {
		return err
	}
	payload, err := s.messageCreatedPayload(ctx, dbtx, m)
	if err != nil {
		return err
	}
	return s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{{
		EventType:       domain.EventMessageCreated,
		ConversationID:  &m.ConversationID,
		EntityID:        &m.ID,
		ActorUserID:     &cmd.UserID,
		AffectedUserIDs: members,
		Payload:         payload,
	}})
}

// messageCreatedPayload builds the §18.5 message.created event data.
func (s *service) messageCreatedPayload(ctx context.Context, q tx.Querier, m domain.Message) ([]byte, error) {
	senderName := ""
	if m.SenderID != nil {
		if users, err := s.deps.Users.ListByIDs(ctx, q, []int64{*m.SenderID}); err == nil && len(users) == 1 {
			senderName = users[0].DisplayName
		}
	}
	payload := map[string]any{
		"id":              m.ID,
		"conversation_id": m.ConversationID,
		"sequence":        m.Sequence,
		"sender_id":       m.SenderID,
		"sender":          map[string]any{"display_name": senderName},
		"type":            string(m.RenderedType()),
		"content":         map[string]any{"text": m.Content},
		"media":           decodeEnvelopePlain(m.AttachmentEnvelope),
		"client_msg_id":   m.ClientMsgID,
		"mentions":        m.Mentions,
		"reply_to_id":     m.ReplyToID,
		"created_at":      m.CreatedAt,
		"edited_at":       m.EditedAt,
		"global_seq":      m.GlobalSeq,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, makeErr(err, "chat: encode message.created payload")
	}
	return b, nil
}
