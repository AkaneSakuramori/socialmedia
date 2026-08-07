package application

import (
	"context"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// ListConversations returns the caller's chat list (API.md §7.1),
// most-recent-first, keyset-paginated. Unread counts are derived from the
// cursors, never stored (DATABASE.md §5.2).
func (s *service) ListConversations(ctx context.Context, cmd ListConversationsCommand) (*ConversationListResult, error) {
	filter := cmd.Filter
	if filter == "" {
		filter = "all"
	}
	switch filter {
	case "all", "pinned", "archived", "groups", "direct":
	default:
		return nil, validationError("filter", "invalid_filter")
	}

	limit := cmd.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	cursor, err := decodeConversationCursor(cmd.Cursor)
	if err != nil {
		return nil, validationError("cursor", "invalid_cursor")
	}

	rows, err := s.deps.Conversations.List(ctx, domain.ConversationListQuery{
		UserID:     cmd.UserID,
		Filter:     filter,
		UnreadOnly: cmd.UnreadOnly,
		Limit:      limit + 1, // +1 to detect has_more
		Cursor:     cursor,
	})
	if err != nil {
		return nil, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	// Resolve counterpart display names for direct chats in one query.
	var nameIDs []int64
	for _, r := range rows {
		if r.Type == domain.ConversationDirect && r.CounterpartID != nil {
			nameIDs = append(nameIDs, *r.CounterpartID)
		}
	}
	names := map[int64]string{}
	if len(nameIDs) > 0 {
		users, err := s.deps.Users.ListByIDs(ctx, nameIDs)
		if err != nil {
			return nil, err
		}
		names = displayNames(users)
	}

	items := make([]ConversationView, 0, len(rows))
	for _, r := range rows {
		items = append(items, s.buildListView(r, names))
	}

	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		enc, err := conversationCursor(domain.ConversationCursor{Activity: last.LastActivity(), ID: last.ID})
		if err != nil {
			return nil, err
		}
		next = &enc
	}

	return &ConversationListResult{Items: items, Next: next, HasMore: hasMore, Limit: limit}, nil
}
