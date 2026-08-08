package application

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// Message cursors are opaque, versioned keyset positions (API.md §2.6). Layout:
// "s:<seq>" scrolls back by per-conversation sequence; "g:<global_seq>" polls
// the sync delta. Sequence and global_seq are both strictly monotonic, so a
// numeric bound is a stable keyset.

const (
	historyCursorPrefix = "s"
	deltaCursorPrefix   = "g"
)

func encodeMessageCursor(prefix string, n int64) string {
	return prefix + ":" + strconv.FormatInt(n, 10)
}

// decodeMessageCursor resolves the opaque cursor into the seek it encodes.
func decodeMessageCursor(s string) (before, after *int64, err error) {
	if s == "" {
		return nil, nil, nil
	}
	prefix, raw, ok := strings.Cut(s, ":")
	if !ok {
		return nil, nil, domain.ErrInvalidCursor
	}
	n, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil || n <= 0 {
		return nil, nil, domain.ErrInvalidCursor
	}
	switch prefix {
	case historyCursorPrefix:
		return &n, nil, nil
	case deltaCursorPrefix:
		return nil, &n, nil
	}
	return nil, nil, domain.ErrInvalidCursor
}

// ListMessages paginates message history (API.md §8.1): keyset on sequence
// (scroll-back) or global_seq (delta poll), strictly ordered ascending within
// the page. Tombstones are included (rendered as "deleted" by the client).
func (s *service) ListMessages(ctx context.Context, cmd ListMessagesCommand) (*MessageListResult, error) {
	if _, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID); err != nil {
		return nil, err
	}
	if _, err := s.deps.Memberships.FindActive(ctx, cmd.ConversationID, cmd.UserID); err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}

	before, after := cmd.BeforeSeq, cmd.AfterGlobalSeq
	if cmd.Cursor != "" {
		b, a, err := decodeMessageCursor(cmd.Cursor)
		if err != nil {
			return nil, err
		}
		before, after = b, a
	}
	if before != nil && after != nil {
		return nil, validationError("cursor", "incompatible_seeks")
	}

	limit := cmd.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	rows, err := s.deps.Messages.ListByConversation(ctx, domain.MessageListQuery{
		ConversationID: cmd.ConversationID,
		BeforeSeq:      before,
		AfterGlobalSeq: after,
		Limit:          limit + 1,
	})
	if err != nil {
		return nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	e, err := s.enrichPage(ctx, rows)
	if err != nil {
		return nil, err
	}

	items := make([]MessageView, 0, len(rows))
	var next *string
	if after != nil {
		// Delta poll: rows arrive in ascending global order; the next cursor is
		// the last global_seq returned.
		for i := range rows {
			items = append(items, s.messageView(rows[i], e))
		}
		if len(rows) > 0 {
			nc := encodeMessageCursor(deltaCursorPrefix, rows[len(rows)-1].GlobalSeq)
			next = &nc
		}
	} else {
		// History: rows arrive newest-first; reverse to ascending wire order.
		// The scroll-back cursor is the oldest sequence returned.
		for i := len(rows) - 1; i >= 0; i-- {
			items = append(items, s.messageView(rows[i], e))
		}
		if hasMore && len(rows) > 0 {
			nc := encodeMessageCursor(historyCursorPrefix, rows[len(rows)-1].Sequence)
			next = &nc
		}
	}

	return &MessageListResult{Items: items, Next: next, HasMore: hasMore, Limit: limit}, nil
}

// GetMessage fetches one message by id (API.md §8.3), gated on membership.
func (s *service) GetMessage(ctx context.Context, cmd GetMessageCommand) (*MessageView, error) {
	m, err := s.deps.Messages.FindByID(ctx, cmd.MessageID)
	if err != nil {
		return nil, err
	}
	if _, err := s.deps.Memberships.FindActive(ctx, m.ConversationID, cmd.UserID); err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, domain.ErrNotMember
		}
		return nil, err
	}
	return s.viewFor(ctx, *m), nil
}
