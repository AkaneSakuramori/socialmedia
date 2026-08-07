package application

import (
	"context"
)

// GetConversation returns one conversation's metadata and membership summary
// (API.md §7.3). Non-members receive NOT_A_MEMBER (403), matching the spec's
// "404 (or 403 for blocked/members-only visibility)".
func (s *service) GetConversation(ctx context.Context, cmd GetConversationCommand) (*ConversationDetail, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	return s.conversationDetailFor(ctx, c, cmd.UserID)
}
