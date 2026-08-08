package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// ListMembers returns the paginated member list with roles (API.md §7.5).
func (s *service) ListMembers(ctx context.Context, cmd ListMembersCommand) (*MemberListResult, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireMember(ctx, c, cmd.UserID); err != nil {
		return nil, err
	}

	limit := cmd.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	cursor, err := decodeMemberCursor(cmd.Cursor)
	if err != nil {
		return nil, validationError("cursor", "invalid_cursor")
	}

	rows, err := s.deps.Memberships.ListMembers(ctx, c.ID, domain.MemberListQuery{
		Limit:  limit + 1,
		Cursor: cursor,
		Q:      cmd.Q,
	})
	if err != nil {
		return nil, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	items := make([]MemberView, 0, len(rows))
	for _, r := range rows {
		items = append(items, MemberView{
			UserID:      strconv.FormatInt(r.UserID, 10),
			DisplayName: r.DisplayName,
			Role:        string(r.Role),
			JoinedAt:    r.JoinedAt,
			IsSelf:      r.UserID == cmd.UserID,
		})
	}

	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		enc, err := memberCursor(domain.MemberCursor{JoinedAt: last.JoinedAt, UserID: last.UserID})
		if err != nil {
			return nil, err
		}
		next = &enc
	}

	return &MemberListResult{Items: items, Next: next, HasMore: hasMore, Limit: limit}, nil
}

// AddMembers adds users to a group (API.md §7.6). Requires admin+ unless the
// group's anyone_can_add setting allows members to invite. Blocked-user checks
// land with the contacts milestone. Adding past the 500-member cap is a 422
// (ErrConversationFull), not a silent partial success.
func (s *service) AddMembers(ctx context.Context, cmd AddMembersCommand) (*AddMembersResult, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	m, err := s.requireMember(ctx, c, cmd.UserID)
	if err != nil {
		return nil, err
	}

	// Empty or invalid input is a validation failure, not a partial success.
	if len(cmd.UserIDs) == 0 {
		return nil, validationError("user_ids", "must_not_be_empty")
	}
	seen := make(map[int64]bool, len(cmd.UserIDs))
	for _, id := range cmd.UserIDs {
		if id <= 0 {
			return nil, validationError("user_ids", "invalid_user_id")
		}
		if seen[id] {
			return nil, validationError("user_ids", "must_be_unique")
		}
		seen[id] = true
	}

	// §7.6 authorization: admin+ unless anyone_can_add.
	if !m.Role.AtLeast(domain.RoleAdmin) && !c.Settings.AnyoneCanAdd {
		return nil, domain.ErrInsufficientRole
	}

	existing, err := s.deps.Memberships.ActiveUserIDs(ctx, s.deps.DB, c.ID)
	if err != nil {
		return nil, err
	}
	existingSet := make(map[int64]bool, len(existing))
	for _, id := range existing {
		existingSet[id] = true
	}

	// Resolve which targets are real users.
	users, err := s.deps.Users.ListByIDs(ctx, s.deps.DB, cmd.UserIDs)
	if err != nil {
		return nil, err
	}
	userSet := make(map[int64]bool, len(users))
	for _, u := range users {
		userSet[u.ID] = true
	}

	// The candidates are the targets that are not the caller, not already
	// members, and are live accounts.
	var addedIDs []int64
	for _, id := range cmd.UserIDs {
		if id == cmd.UserID || existingSet[id] || !userSet[id] {
			continue
		}
		addedIDs = append(addedIDs, id)
	}

	// Hard cap: total active membership may not exceed 500 (API.md §7.6).
	if len(existing)+len(addedIDs) > maxTotalMembers {
		return nil, domain.ErrConversationFull
	}

	skipped := buildSkipped(cmd.UserID, cmd.UserIDs, existingSet, userSet)

	if len(addedIDs) == 0 {
		return &AddMembersResult{Added: []string{}, Skipped: skipped}, nil
	}

	affected := append(append([]int64{}, existing...), addedIDs...)
	payload, err := json.Marshal(map[string]any{
		"conversation_id": c.ID,
		"added_user_ids":  addedIDs,
		"actor_user_id":   cmd.UserID,
	})
	if err != nil {
		return nil, makeErr(err, "chat: encode outbox payload")
	}

	add := make([]*domain.Membership, 0, len(addedIDs))
	for _, id := range addedIDs {
		add = append(add, newMembership(c.ID, id, domain.RoleMember, s.now()))
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin add-members transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Memberships.AddMany(ctx, dbtx, add); err != nil {
		return nil, makeErr(err, "chat: add members")
	}
	convID := c.ID
	if err := s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{
		{
			EventType:       domain.EventConversationMembership,
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
		return nil, makeErr(err, "chat: commit add-members transaction")
	}

	added := make([]string, 0, len(addedIDs))
	for _, id := range addedIDs {
		added = append(added, strconv.FormatInt(id, 10))
	}
	return &AddMembersResult{Added: added, Skipped: skipped}, nil
}

// RemoveMember removes a member or lets the caller leave (API.md §7.7).
// Self-leave is always allowed; removing others requires admin+ (only the
// owner may remove an admin; the owner cannot be removed). When the last
// member leaves, the conversation is tombstoned (kept for history).
func (s *service) RemoveMember(ctx context.Context, cmd RemoveMemberCommand) error {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return err
	}
	caller, err := s.requireMember(ctx, c, cmd.UserID)
	if err != nil {
		return err
	}

	target, err := s.deps.Memberships.FindActive(ctx, c.ID, cmd.TargetUserID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return domain.ErrNotMember
		}
		return err
	}

	if cmd.TargetUserID != cmd.UserID {
		switch {
		case target.Role == domain.RoleOwner:
			return domain.ErrCannotRemoveOwner
		case target.Role == domain.RoleAdmin && caller.Role != domain.RoleOwner:
			return domain.ErrInsufficientRole
		case caller.Role == domain.RoleMember:
			return domain.ErrInsufficientRole
		}
	}

	now := s.now()
	leftAt := now
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return makeErr(err, "chat: begin remove-members transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Memberships.Remove(ctx, dbtx, c.ID, cmd.TargetUserID, leftAt); err != nil {
		return makeErr(err, "chat: remove member")
	}

	remaining, err := s.deps.Memberships.CountActive(ctx, c.ID)
	if err != nil {
		return err
	}

	// Last member leaving tombstones the conversation for history.
	if remaining == 0 {
		if err := s.deps.Conversations.Tombstone(ctx, dbtx, c.ID, now); err != nil {
			return makeErr(err, "chat: tombstone conversation")
		}
	}

	affected, err := s.deps.Memberships.ActiveUserIDs(ctx, dbtx, c.ID)
	if err != nil {
		return err
	}
	if remaining == 0 {
		affected = append(affected, cmd.TargetUserID)
	}
	payload, err := json.Marshal(map[string]any{
		"conversation_id": c.ID,
		"removed_user_id": cmd.TargetUserID,
		"actor_user_id":   cmd.UserID,
	})
	if err != nil {
		return makeErr(err, "chat: encode outbox payload")
	}

	convID := c.ID
	if err := s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{
		{
			EventType:       domain.EventConversationMembership,
			ConversationID:  &convID,
			EntityID:        &convID,
			ActorUserID:     &cmd.UserID,
			AffectedUserIDs: affected,
			Payload:         payload,
		},
	}); err != nil {
		return makeErr(err, "chat: append outbox")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return makeErr(err, "chat: commit remove-members transaction")
	}
	return nil
}

// ChangeMemberRole changes a member's role (API.md §7.8). Only the owner may
// grant or revoke the owner role; admins may manage member roles. The owner
// cannot be demoted without a transfer (M1 scope).
func (s *service) ChangeMemberRole(ctx context.Context, cmd ChangeMemberRoleCommand) (*RoleChangeResult, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	caller, err := s.requireMember(ctx, c, cmd.UserID)
	if err != nil {
		return nil, err
	}

	newRole, err := domain.ParseRole(cmd.Role)
	if err != nil {
		return nil, validationError("role", "invalid_role")
	}

	target, err := s.deps.Memberships.FindActive(ctx, c.ID, cmd.TargetUserID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}

	if newRole == domain.RoleOwner && caller.Role != domain.RoleOwner {
		return nil, domain.ErrOnlyOwnerMayChangeOwner
	}
	if !caller.Role.AtLeast(domain.RoleAdmin) {
		return nil, domain.ErrInsufficientRole
	}
	if target.Role == domain.RoleOwner {
		return nil, domain.ErrCannotDemoteOwner
	}
	if target.Role == domain.RoleAdmin && caller.Role != domain.RoleOwner {
		return nil, domain.ErrInsufficientRole
	}

	target.Role = newRole

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin role-change transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Memberships.Update(ctx, dbtx, target); err != nil {
		return nil, makeErr(err, "chat: update member role")
	}

	affected, err := s.deps.Memberships.ActiveUserIDs(ctx, dbtx, c.ID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"conversation_id": c.ID,
		"member_user_id":  cmd.TargetUserID,
		"role":            string(newRole),
		"actor_user_id":   cmd.UserID,
	})
	if err != nil {
		return nil, makeErr(err, "chat: encode outbox payload")
	}

	convID := c.ID
	if err := s.deps.ChangeLog.Append(ctx, dbtx, []domain.ChangeLogEntry{
		{
			EventType:       domain.EventConversationMembership,
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
		return nil, makeErr(err, "chat: commit role-change transaction")
	}
	return &RoleChangeResult{UserID: strconv.FormatInt(cmd.TargetUserID, 10), Role: string(newRole)}, nil
}

// requireMember loads the caller's active membership or NOT_A_MEMBER.
func (s *service) requireMember(ctx context.Context, c *domain.Conversation, userID int64) (*domain.Membership, error) {
	m, err := s.deps.Memberships.FindActive(ctx, c.ID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}
	return m, nil
}

// buildSkipped returns the deterministic per-user skip list for AddMembers.
func buildSkipped(caller int64, targets []int64, existing, userSet map[int64]bool) []SkippedMember {
	var skipped []SkippedMember
	for _, id := range targets {
		reason := ""
		switch {
		case id == caller:
			reason = "already_member"
		case existing[id]:
			reason = "already_member"
		case !userSet[id]:
			reason = "unknown_user"
		}
		if reason != "" {
			skipped = append(skipped, SkippedMember{UserID: strconv.FormatInt(id, 10), Reason: reason})
		}
	}
	return skipped
}
