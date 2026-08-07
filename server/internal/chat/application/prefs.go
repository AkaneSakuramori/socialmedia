package application

import (
	"context"
)

// SetMute mutes or unmutes notifications for the caller (API.md §7.9). A mute
// deadline must be in the future. Unread counts still accumulate while muted.
func (s *service) SetMute(ctx context.Context, cmd SetMuteCommand) (*MuteResult, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	m, err := s.requireMember(ctx, c, cmd.UserID)
	if err != nil {
		return nil, err
	}
	if cmd.Until != nil && !cmd.Until.After(s.now()) {
		return nil, validationError("until", "must_be_in_the_future")
	}

	m.MutedUntil = cmd.Until

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin mute transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Memberships.Update(ctx, dbtx, m); err != nil {
		return nil, makeErr(err, "chat: update mute")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit mute transaction")
	}

	return &MuteResult{MutedUntil: m.MutedUntil}, nil
}

// SetPin pins or unpins the conversation for the caller (API.md §7.10).
// Pin and archive are mutually exclusive; pinning clears an archive.
func (s *service) SetPin(ctx context.Context, cmd SetPinCommand) (*PinResult, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	m, err := s.requireMember(ctx, c, cmd.UserID)
	if err != nil {
		return nil, err
	}

	if cmd.Pinned {
		now := s.now()
		m.PinnedAt = &now
		m.ArchivedAt = nil // mutually exclusive (DATABASE.md §5.2 CHECK)
	} else {
		m.PinnedAt = nil
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin pin transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Memberships.Update(ctx, dbtx, m); err != nil {
		return nil, makeErr(err, "chat: update pin")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit pin transaction")
	}

	return &PinResult{IsPinned: m.PinnedAt != nil}, nil
}

// SetArchive archives or unarchives the conversation for the caller
// (API.md §7.11). Archiving clears a pin (mutual exclusion); a new incoming
// message auto-unarchives (messaging milestone).
func (s *service) SetArchive(ctx context.Context, cmd SetArchiveCommand) (*ArchiveResult, error) {
	c, err := s.deps.Conversations.FindByID(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	m, err := s.requireMember(ctx, c, cmd.UserID)
	if err != nil {
		return nil, err
	}

	if cmd.Archived {
		now := s.now()
		m.ArchivedAt = &now
		m.PinnedAt = nil // mutually exclusive (DATABASE.md §5.2 CHECK)
	} else {
		m.ArchivedAt = nil
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, makeErr(err, "chat: begin archive transaction")
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Memberships.Update(ctx, dbtx, m); err != nil {
		return nil, makeErr(err, "chat: update archive")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, makeErr(err, "chat: commit archive transaction")
	}

	return &ArchiveResult{IsArchived: m.ArchivedAt != nil}, nil
}
