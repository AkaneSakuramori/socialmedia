package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// UpdateConversation patches group settings (API.md §7.4): title,
// avatar_media_id, and settings. Only owner/admin roles may mutate a
// conversation (INSUFFICIENT_ROLE otherwise).
func (s *service) UpdateConversation(ctx context.Context, cmd UpdateConversationCommand) (*ConversationDetail, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireAdmin(ctx, c, cmd.UserID); err != nil {
		return nil, err
	}

	if c.Type == domain.ConversationDirect {
		// Direct chats derive their title and have no group settings.
		if cmd.TitleSet {
			return nil, validationError("title", "direct_conversation_has_no_title")
		}
		if cmd.Settings != nil {
			return nil, validationError("settings", "direct_conversation_has_no_settings")
		}
	}

	if cmd.TitleSet {
		if c.Type != domain.ConversationGroup {
			return nil, validationError("title", "direct_conversation_has_no_title")
		}
		if cmd.Title == nil || strings.TrimSpace(*cmd.Title) == "" {
			return nil, domain.ErrGroupTitleRequired
		}
		c.Title = cmd.Title
	}
	if cmd.AvatarSet {
		c.PhotoMediaID = cmd.AvatarMediaID
	}
	if cmd.AvatarCleared {
		c.PhotoMediaID = nil
	}
	if cmd.Settings != nil {
		if err := applySettingsPatch(&c.Settings, cmd.Settings); err != nil {
			return nil, err
		}
	}

	c.UpdatedAt = s.now()

	affected, err := s.deps.Memberships.ActiveUserIDs(ctx, s.deps.DB, c.ID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"conversation_id": c.ID,
		"title":           c.Title,
		"settings":        c.Settings,
		"updated_at":      c.UpdatedAt,
	})
	if err != nil {
		return nil, makeErr(err, "chat: encode outbox payload")
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin update transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	convID := c.ID
	if err := s.deps.Conversations.Update(ctx, dbtx, c); err != nil {
		return nil, makeErr(err, "chat: update conversation")
	}
	if err := s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{
		{
			EventType:       domain.EventConversationSettings,
			ConversationID:  &convID,
			EntityID:        &convID,
			ActorUserID:     &cmd.UserID,
			AffectedUserIDs: affected,
			Payload:         payload,
		},
	}); err != nil {
		return nil, makeErr(err, "chat: append outbox")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit update transaction")
	}

	return s.conversationDetailFor(ctx, c, cmd.UserID)
}

// requireAdmin loads the caller's active membership and enforces the §7.4
// owner/admin gate.
func (s *service) requireAdmin(ctx context.Context, c *domain.Conversation, userID int64) (*domain.Membership, error) {
	m, err := s.deps.Memberships.FindActive(ctx, c.ID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}
	if !m.Role.AtLeast(domain.RoleAdmin) {
		return nil, domain.ErrInsufficientRole
	}
	return m, nil
}

// applySettingsPatch validates and applies the optional §7.4 settings.
func applySettingsPatch(current *domain.Settings, patch *SettingsPatch) error {
	if patch.SlowModeSeconds != nil {
		if *patch.SlowModeSeconds < 0 {
			return validationError("settings.slow_mode_seconds", "must_be_non_negative")
		}
		current.SlowModeSeconds = *patch.SlowModeSeconds
	}
	if patch.AnyoneCanAdd != nil {
		current.AnyoneCanAdd = *patch.AnyoneCanAdd
	}
	if patch.HistoryVisible != nil {
		hv := domain.HistoryVisible(*patch.HistoryVisible)
		if !hv.Valid() {
			return domain.ErrInvalidHistoryVisible
		}
		current.HistoryVisible = hv
	}
	return nil
}
