package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// maxDistinctReactions bounds the distinct emoji per message (API.md §8.6:
// "max 20 distinct emoji per message").
const maxDistinctReactions = 20

// AddReaction adds a reaction (API.md §8.6). A duplicate (message,user,emoji)
// is a no-op 200. Re-adding an existing emoji never trips the 20-distinct limit.
func (s *service) AddReaction(ctx context.Context, cmd ReactionCommand) (*ReactionResult, error) {
	if err := validateEmoji(cmd.Emoji); err != nil {
		return nil, err
	}
	m, err := s.deps.Messages.FindByID(ctx, cmd.MessageID)
	if err != nil {
		return nil, err
	}
	if err := s.requireActiveMember(ctx, m.ConversationID, cmd.UserID); err != nil {
		return nil, err
	}
	if m.Deleted() {
		return nil, domain.ErrMessageDeleted
	}

	already, err := s.deps.Reactions.Count(ctx, m.ID, cmd.Emoji)
	if err != nil {
		return nil, err
	}
	if already == 0 {
		n, err := s.deps.Reactions.DistinctEmoji(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		if n >= maxDistinctReactions {
			return nil, domain.ErrReactionLimit
		}
	}

	reactionID, err := s.deps.IDs.NextID()
	if err != nil {
		return nil, makeErr(err, "chat: allocate reaction id")
	}
	now := s.now()

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin add reaction transaction")
	}
	defer dbtx.Rollback(ctx)

	added, err := s.deps.Reactions.Add(ctx, dbtx, &domain.ReactionRow{
		ID: reactionID, MessageID: m.ID, UserID: cmd.UserID, Emoji: cmd.Emoji, CreatedAt: now,
	})
	if err != nil {
		return nil, makeErr(err, "chat: add reaction")
	}
	if added {
		if err := s.appendReactionEvent(ctx, dbtx, *m, cmd.UserID, cmd.Emoji, true); err != nil {
			return nil, err
		}
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit add reaction transaction")
	}

	count, err := s.deps.Reactions.Count(ctx, m.ID, cmd.Emoji)
	if err != nil {
		return nil, err
	}
	return &ReactionResult{
		MessageID: strconv.FormatInt(m.ID, 10),
		Emoji:     cmd.Emoji,
		Count:     count,
	}, nil
}

// RemoveReaction removes the caller's reaction (API.md §8.7).
func (s *service) RemoveReaction(ctx context.Context, cmd ReactionCommand) (*ReactionResult, error) {
	if err := validateEmoji(cmd.Emoji); err != nil {
		return nil, err
	}
	m, err := s.deps.Messages.FindByID(ctx, cmd.MessageID)
	if err != nil {
		return nil, err
	}
	if err := s.requireActiveMember(ctx, m.ConversationID, cmd.UserID); err != nil {
		return nil, err
	}
	if m.Deleted() {
		return nil, domain.ErrMessageDeleted
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin remove reaction transaction")
	}
	defer dbtx.Rollback(ctx)

	removed, err := s.deps.Reactions.Remove(ctx, dbtx, m.ID, cmd.UserID, cmd.Emoji)
	if err != nil {
		return nil, makeErr(err, "chat: remove reaction")
	}
	if removed {
		if err := s.appendReactionEvent(ctx, dbtx, *m, cmd.UserID, cmd.Emoji, false); err != nil {
			return nil, err
		}
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit remove reaction transaction")
	}

	count, err := s.deps.Reactions.Count(ctx, m.ID, cmd.Emoji)
	if err != nil {
		return nil, err
	}
	return &ReactionResult{
		MessageID: strconv.FormatInt(m.ID, 10),
		Emoji:     cmd.Emoji,
		Count:     count,
	}, nil
}

// ListReactions lists the reactors for a message + emoji (API.md §8.8).
func (s *service) ListReactions(ctx context.Context, cmd ListReactionsCommand) (*ReactionsResult, error) {
	if err := validateEmoji(cmd.Emoji); err != nil {
		return nil, err
	}
	m, err := s.deps.Messages.FindByID(ctx, cmd.MessageID)
	if err != nil {
		return nil, err
	}
	if err := s.requireActiveMember(ctx, m.ConversationID, cmd.UserID); err != nil {
		return nil, err
	}

	reactors, err := s.deps.Reactions.Reactors(ctx, m.ID, cmd.Emoji)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(reactors))
	for _, r := range reactors {
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

	out := make([]ReactorView, 0, len(reactors))
	for _, r := range reactors {
		out = append(out, ReactorView{
			UserID:      strconv.FormatInt(r.UserID, 10),
			DisplayName: names[r.UserID],
			At:          r.At,
		})
	}
	return &ReactionsResult{Emoji: cmd.Emoji, Reactors: out}, nil
}

// validateEmoji rejects empty or malformed reaction emoji (API.md §8.6).
func validateEmoji(emoji string) error {
	emoji = strings.TrimSpace(emoji)
	if emoji == "" {
		return validationError("emoji", "required")
	}
	if len([]rune(emoji)) > 16 {
		return validationError("emoji", "too_long")
	}
	return nil
}

// requireActiveMember is the shared reaction membership gate (by id, no conversation load).
func (s *service) requireActiveMember(ctx context.Context, conversationID, userID int64) error {
	_, err := s.deps.Memberships.FindActive(ctx, conversationID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return domain.ErrNotMember
		}
		return err
	}
	return nil
}

// appendReactionEvent writes the message.reaction outbox row (§18.8 data).
func (s *service) appendReactionEvent(ctx context.Context, dbtx tx.Tx, m domain.Message, actorID int64, emoji string, added bool) error {
	members, err := s.deps.Memberships.ActiveUserIDs(ctx, dbtx, m.ConversationID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"message_id":      m.ID,
		"conversation_id": m.ConversationID,
		"sequence":        m.Sequence,
		"emoji":           emoji,
		"actor_id":        actorID,
		"added":           added,
		"global_seq":      m.GlobalSeq,
	})
	if err != nil {
		return err
	}
	return s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{{
		EventType:       domain.EventMessageReaction,
		ConversationID:  &m.ConversationID,
		EntityID:        &m.ID,
		ActorUserID:     &actorID,
		AffectedUserIDs: members,
		Payload:         payload,
	}})
}
