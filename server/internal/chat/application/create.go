package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// CreateConversation creates a direct or group conversation (API.md §7.2):
// conversation + membership rows + the durable sequence row + change_log outbox
// events, all in one transaction. A direct conversation with the same
// counterpart returns the existing one (Created=false → HTTP 200) instead of a
// duplicate.
func (s *service) CreateConversation(ctx context.Context, cmd CreateConversationCommand) (*CreateConversationResult, error) {
	ctype, err := domain.ParseConversationType(cmd.Type)
	if err != nil {
		return nil, validationError("type", "invalid_conversation_type")
	}

	others, err := s.validateParticipants(ctx, cmd.UserID, ctype, cmd.ParticipantIDs)
	if err != nil {
		return nil, err
	}
	if ctype == domain.ConversationGroup {
		if cmd.Title == nil || strings.TrimSpace(*cmd.Title) == "" {
			return nil, domain.ErrGroupTitleRequired
		}
	}

	// §7.2 dedup: an active direct chat with the same counterpart is returned,
	// never duplicated (either direction).
	if ctype == domain.ConversationDirect {
		other := others[0]
		existing, err := s.deps.Conversations.FindDirectPair(ctx, cmd.UserID, other)
		switch {
		case err == nil:
			view, err := s.conversationListView(ctx, existing, cmd.UserID)
			if err != nil {
				return nil, err
			}
			return &CreateConversationResult{View: view, Created: false}, nil
		case !errors.Is(err, domain.ErrConversationNotFound):
			return nil, err
		}
	}

	now := s.now()
	convID, err := s.deps.IDs.NextID()
	if err != nil {
		return nil, makeErr(err, "chat: allocate conversation id")
	}

	conv := &domain.Conversation{
		ID:           convID,
		Type:         ctype,
		Title:        cmd.Title,
		PhotoMediaID: cmd.AvatarMediaID,
		CreatedBy:    cmd.UserID,
		Settings:     domain.DefaultSettings(),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	callerRole := domain.RoleMember
	if ctype == domain.ConversationGroup {
		callerRole = domain.RoleOwner
	}
	members := []*domain.Membership{newMembership(convID, cmd.UserID, callerRole, now)}
	affected := []int64{cmd.UserID}
	for _, o := range others {
		members = append(members, newMembership(convID, o, domain.RoleMember, now))
		affected = append(affected, o)
	}

	createdPayload, err := json.Marshal(map[string]any{
		"conversation_id": convID,
		"type":            string(ctype),
		"title":           conv.Title,
		"created_by":      cmd.UserID,
		"created_at":      now,
	})
	if err != nil {
		return nil, makeErr(err, "chat: encode outbox payload")
	}

	events := []domain.ChangeLogEntry{
		{
			EventType:       domain.EventConversationCreated,
			ConversationID:  &convID,
			EntityID:        &convID,
			ActorUserID:     &cmd.UserID,
			AffectedUserIDs: affected,
			Payload:         createdPayload,
		},
		{
			EventType:       domain.EventConversationMembership,
			ConversationID:  &convID,
			EntityID:        &convID,
			ActorUserID:     &cmd.UserID,
			AffectedUserIDs: affected,
			Payload:         createdPayload,
		},
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin create transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Conversations.Create(ctx, dbtx, conv); err != nil {
		return nil, makeErr(err, "chat: insert conversation")
	}
	if err := s.deps.Memberships.AddMany(ctx, dbtx, members); err != nil {
		return nil, makeErr(err, "chat: insert memberships")
	}
	if err := s.deps.Sequences.Init(ctx, dbtx, convID); err != nil {
		return nil, makeErr(err, "chat: init sequence")
	}
	if err := s.deps.ChangeLog.Append(ctx, dbtx, events); err != nil {
		return nil, makeErr(err, "chat: append outbox")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit create transaction")
	}

	row := domain.ConversationRow{Conversation: *conv, Membership: *members[0]}
	if ctype == domain.ConversationDirect {
		row.CounterpartID = &others[0]
	}
	users, err := s.deps.Users.ListByIDs(ctx, affected)
	if err != nil {
		return nil, makeErr(err, "chat: resolve participant names")
	}

	return &CreateConversationResult{
		View:    s.buildListView(row, displayNames(users)),
		Created: true,
	}, nil
}

// conversationListView builds the §7.1 chat-list item for a single
// conversation (used by create dedup and by no other read path).
func (s *service) conversationListView(ctx context.Context, c *domain.Conversation, userID int64) (ConversationView, error) {
	m, err := s.deps.Memberships.FindActive(ctx, c.ID, userID)
	if err != nil {
		return ConversationView{}, err
	}
	row := domain.ConversationRow{Conversation: *c, Membership: *m}
	if c.Type == domain.ConversationDirect {
		// The counterpart is the other active member; the dedup path guarantees
		// exactly two members for a direct chat.
		ms, err := s.deps.Memberships.ListMembers(ctx, c.ID, domain.MemberListQuery{Limit: 2})
		if err != nil {
			return ConversationView{}, err
		}
		for _, mr := range ms {
			if mr.UserID != userID {
				row.CounterpartID = &mr.UserID
				break
			}
		}
	}
	names := map[int64]string{}
	if row.CounterpartID != nil {
		users, err := s.deps.Users.ListByIDs(ctx, []int64{*row.CounterpartID})
		if err != nil {
			return ConversationView{}, err
		}
		names = displayNames(users)
	}
	return s.buildListView(row, names), nil
}

// conversationDetailFor loads the §7.3 detail view, gating on membership.
func (s *service) conversationDetailFor(ctx context.Context, c *domain.Conversation, userID int64) (*ConversationDetail, error) {
	m, err := s.deps.Memberships.FindActive(ctx, c.ID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}

	count, err := s.deps.Memberships.CountActive(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.deps.Memberships.ListMembers(ctx, c.ID, domain.MemberListQuery{Limit: memberPreviewLimit})
	if err != nil {
		return nil, err
	}

	preview := make([]MemberPreviewItem, 0, len(rows))
	var directTitle *string
	for _, r := range rows {
		preview = append(preview, MemberPreviewItem{
			UserID:      fmt.Sprintf("%d", r.UserID),
			DisplayName: r.DisplayName,
		})
		// Direct chats derive their title from the counterpart's name.
		if c.Type == domain.ConversationDirect && r.UserID != userID {
			name := r.DisplayName
			directTitle = &name
		}
	}

	detail := s.buildDetailView(*c, *m, count, preview)
	if c.Type == domain.ConversationDirect {
		detail.Title = directTitle
	}
	return &detail, nil
}
